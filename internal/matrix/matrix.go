package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wiregram/internal/backend"
	"wiregram/internal/config"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

type MxBackend struct {
	Homeserver  string
	AccessToken string
	UserID      string
	DeviceID    string
	// Store and StoreKey persist the sync token; a nil Store keeps it in memory.
	Store    *store.DB
	StoreKey string

	syncToken string

	mu           sync.Mutex
	subscribed   string
	typing       map[string]time.Time
	unreads      map[string]int
	rooms        map[string]*mxRoom
	userCache    map[string]string
	dmRooms      map[string]bool
	ownReactions map[string]string // "eventID\x00emoji" → reaction event ID
	recentEmoji  []string

	events backend.Bus

	client     *http.Client
	syncCancel context.CancelFunc
	txnCounter int64
}

type mxRoom struct {
	name          string
	members       map[string]string // userID → display name
	lastAt        time.Time
	prevBatch     string
	paginationEnd string
}

// Sync response types.

type mxSyncResponse struct {
	NextBatch   string      `json:"next_batch"`
	Rooms       mxSyncRooms `json:"rooms"`
	AccountData mxEventList `json:"account_data"`
}

type mxSyncRooms struct {
	Join map[string]mxSyncJoinedRoom `json:"join"`
}

type mxSyncJoinedRoom struct {
	State               mxEventList           `json:"state"`
	Timeline            mxSyncTimeline        `json:"timeline"`
	Ephemeral           mxEventList           `json:"ephemeral"`
	UnreadNotifications mxUnreadNotifications `json:"unread_notifications"`
}

type mxSyncTimeline struct {
	Events    []mxTimelineEvent `json:"events"`
	PrevBatch string            `json:"prev_batch"`
	Limited   bool              `json:"limited"`
}

type mxEventList struct {
	Events []mxTimelineEvent `json:"events"`
}

type mxUnreadNotifications struct {
	NotificationCount int `json:"notification_count"`
	HighlightCount    int `json:"highlight_count"`
}

type mxTimelineEvent struct {
	EventID        string         `json:"event_id"`
	Type           string         `json:"type"`
	Sender         string         `json:"sender"`
	OriginServerTS int64          `json:"origin_server_ts"`
	Content        map[string]any `json:"content"`
	Unsigned       map[string]any `json:"unsigned,omitempty"`
	StateKey       *string        `json:"state_key,omitempty"`
	Redacts        string         `json:"redacts,omitempty"`
}

type mxMessagesResponse struct {
	Start string            `json:"start"`
	End   string            `json:"end"`
	Chunk []mxTimelineEvent `json:"chunk"`
	State []mxTimelineEvent `json:"state"`
}

func NewMXBackend(homeserver string) *MxBackend {
	return &MxBackend{
		Homeserver:   strings.TrimRight(homeserver, "/"),
		typing:       make(map[string]time.Time),
		unreads:      make(map[string]int),
		rooms:        make(map[string]*mxRoom),
		userCache:    make(map[string]string),
		dmRooms:      make(map[string]bool),
		ownReactions: make(map[string]string),
		client:       &http.Client{Timeout: 120 * time.Second},
	}
}

func (mx *MxBackend) Name() string { return "matrix" }

func (mx *MxBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		Forward:       false,
		DeleteForMe:   false,
		OutboxReceipt: true,
		ViewOnce:      false,
		Reactions:     true,
		Edit:          true,
		Pin:           false,
		Upload:        true,
		Search:        true,
		Typing:        true,
		DeleteChat:    false,
		Archive:       false,
	}
}

// API helpers.

func (mx *MxBackend) api(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, mx.Homeserver+"/_matrix/client/v3"+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mx.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+mx.AccessToken)
	}
	return mx.client.Do(req)
}

func (mx *MxBackend) apiJSON(ctx context.Context, method, path string, body, result any) error {
	resp, err := mx.api(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		var rateLimit struct {
			RetryAfterMS int `json:"retry_after_ms"`
		}
		json.NewDecoder(resp.Body).Decode(&rateLimit)
		delay := time.Duration(rateLimit.RetryAfterMS) * time.Millisecond
		if delay < time.Second {
			delay = time.Second
		}
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		return mx.apiJSON(ctx, method, path, body, result)
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("Matrix API %s: %s", path, string(data))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (mx *MxBackend) nextTxnID() string {
	return fmt.Sprintf("wg-%d", atomic.AddInt64(&mx.txnCounter, 1))
}

// Authentication.

func (mx *MxBackend) resolveHomeserver(ctx context.Context) error {
	wellKnownURL := mx.Homeserver + "/.well-known/matrix/client"
	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		return nil
	}
	resp, err := mx.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var wk struct {
		Homeserver struct {
			BaseURL string `json:"base_url"`
		} `json:"m.homeserver"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wk); err != nil {
		return nil
	}
	if wk.Homeserver.BaseURL != "" {
		mx.Homeserver = strings.TrimRight(wk.Homeserver.BaseURL, "/")
	}
	return nil
}

func (mx *MxBackend) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	if mx.AccessToken != "" {
		var whoami struct {
			UserID   string `json:"user_id"`
			DeviceID string `json:"device_id"`
		}
		if err := mx.apiJSON(ctx, "GET", "/account/whoami", nil, &whoami); err == nil {
			mx.UserID = whoami.UserID
			mx.DeviceID = whoami.DeviceID
			return nil
		}
		mx.AccessToken = ""
	}

	if prompt == nil {
		return fmt.Errorf("Matrix account needs authentication")
	}

	username, err := prompt("Username (e.g. @user:server or user): ", false)
	if err != nil {
		return fmt.Errorf("read username: %w", err)
	}
	username = strings.TrimSpace(username)

	password, err := prompt("Password: ", true)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	if err := mx.resolveHomeserver(ctx); err != nil {
		report(err)
	}

	loginBody := map[string]any{
		"type": "m.login.password",
		"identifier": map[string]string{
			"type": "m.id.user",
			"user": username,
		},
		"password":                    strings.TrimSpace(password),
		"initial_device_display_name": "wiregram",
	}

	var loginResp struct {
		UserID      string `json:"user_id"`
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
	}
	if err := mx.apiJSON(ctx, "POST", "/login", loginBody, &loginResp); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	mx.AccessToken = loginResp.AccessToken
	mx.UserID = loginResp.UserID
	mx.DeviceID = loginResp.DeviceID
	return nil
}

func (mx *MxBackend) Identity() backend.AccountIdentity {
	return backend.AccountIdentity{Name: mx.UserID}
}

func (mx *MxBackend) AccountLabel() string {
	host := mx.Homeserver
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if mx.UserID != "" {
		return fmt.Sprintf("[MX] %s", mx.UserID)
	}
	return fmt.Sprintf("[MX] %s", host)
}

func (mx *MxBackend) AutoLockEnabled() bool { return false }

func (mx *MxBackend) Logout(ctx context.Context) error {
	if err := mx.apiJSON(ctx, "POST", "/logout", struct{}{}, nil); err != nil {
		return fmt.Errorf("log out of Matrix: %w", err)
	}
	return nil
}

// Run + sync loop.

func (mx *MxBackend) Run(ctx context.Context) error {
	mx.StartSync(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (mx *MxBackend) StartSync(ctx context.Context) {
	syncCtx, cancel := context.WithCancel(ctx)
	mx.syncCancel = cancel
	go func() {
		delay := time.Second
		for syncCtx.Err() == nil {
			if err := mx.runSync(syncCtx); err != nil {
				if syncCtx.Err() != nil {
					return
				}
			}
			select {
			case <-syncCtx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, 30*time.Second)
		}
	}()
}

func (mx *MxBackend) runSync(ctx context.Context) error {
	mx.loadSyncToken()
	for ctx.Err() == nil {
		params := url.Values{}
		if mx.syncToken != "" {
			params.Set("since", mx.syncToken)
			params.Set("timeout", "30000")
		} else {
			params.Set("timeout", "0")
			params.Set("filter", `{"room":{"timeline":{"limit":1}}}`)
		}
		req, err := http.NewRequestWithContext(ctx, "GET",
			mx.Homeserver+"/_matrix/client/v3/sync?"+params.Encode(), nil)
		if err != nil {
			return fmt.Errorf("create sync request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+mx.AccessToken)
		resp, err := mx.client.Do(req)
		if err != nil {
			return fmt.Errorf("sync: %w", err)
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("sync: HTTP %d: %s", resp.StatusCode, string(data))
		}
		var syncResp mxSyncResponse
		err = json.NewDecoder(resp.Body).Decode(&syncResp)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode sync: %w", err)
		}
		mx.processSyncResponse(&syncResp)
		mx.syncToken = syncResp.NextBatch
		mx.saveSyncToken()
	}
	return ctx.Err()
}

func (mx *MxBackend) processSyncResponse(resp *mxSyncResponse) {
	for _, ev := range resp.AccountData.Events {
		if ev.Type == "m.direct" {
			mx.processDirect(ev.Content)
		}
	}
	for roomID, room := range resp.Rooms.Join {
		mx.processJoinedRoom(roomID, room)
	}
}

func (mx *MxBackend) processDirect(content map[string]any) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	mx.dmRooms = make(map[string]bool)
	for _, v := range content {
		rooms, ok := v.([]any)
		if !ok {
			continue
		}
		for _, r := range rooms {
			if roomID, ok := r.(string); ok {
				mx.dmRooms[roomID] = true
			}
		}
	}
}

func (mx *MxBackend) processJoinedRoom(roomID string, joined mxSyncJoinedRoom) {
	mx.mu.Lock()
	room := mx.rooms[roomID]
	if room == nil {
		room = &mxRoom{members: make(map[string]string)}
		mx.rooms[roomID] = room
	}
	if joined.Timeline.PrevBatch != "" {
		room.prevBatch = joined.Timeline.PrevBatch
	}
	mx.unreads[roomID] = joined.UnreadNotifications.NotificationCount
	subscribed := mx.subscribed
	mx.mu.Unlock()

	for _, ev := range joined.State.Events {
		mx.processStateEvent(roomID, ev)
	}
	for _, ev := range joined.Timeline.Events {
		if ev.StateKey != nil {
			mx.processStateEvent(roomID, ev)
		}
		mx.processTimelineEvent(roomID, ev, subscribed)
	}
	for _, ev := range joined.Ephemeral.Events {
		if ev.Type == "m.typing" {
			mx.processTypingEvent(roomID, ev, subscribed)
		}
	}
	mx.mu.Lock()
	if len(joined.Timeline.Events) > 0 {
		last := joined.Timeline.Events[len(joined.Timeline.Events)-1]
		room.lastAt = time.UnixMilli(last.OriginServerTS)
	}
	mx.mu.Unlock()
}

func (mx *MxBackend) processStateEvent(roomID string, ev mxTimelineEvent) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	room := mx.rooms[roomID]
	if room == nil {
		return
	}
	switch ev.Type {
	case "m.room.name":
		if name, ok := ev.Content["name"].(string); ok {
			room.name = name
		}
	case "m.room.member":
		if ev.StateKey == nil {
			return
		}
		userID := *ev.StateKey
		membership, _ := ev.Content["membership"].(string)
		displayName, _ := ev.Content["displayname"].(string)
		if membership == "join" {
			room.members[userID] = displayName
			if displayName != "" {
				mx.userCache[userID] = displayName
			}
		} else if membership == "leave" || membership == "ban" {
			delete(room.members, userID)
		}
	}
}

func (mx *MxBackend) processTimelineEvent(roomID string, ev mxTimelineEvent, subscribed string) {
	chat := backend.ChatID{Backend: "matrix", Id: roomID}
	switch ev.Type {
	case "m.room.message":
		relatesTo := mxRelatesTo(ev.Content)
		if relatesTo != nil {
			relType, _ := relatesTo["rel_type"].(string)
			if relType == "m.replace" {
				targetID, _ := relatesTo["event_id"].(string)
				if targetID == "" {
					return
				}
				msg := mx.eventToMessage(ev)
				msg.ID = targetID
				if newContent, ok := ev.Content["m.new_content"].(map[string]any); ok {
					if body, ok := newContent["body"].(string); ok {
						msg.Text = body
					}
				}
				msg.EditDate = int(ev.OriginServerTS / 1000)
				mx.events.Publish(backend.Event{
					Kind: backend.EventEdit, Chat: chat, DM: mx.isDirect(roomID), Message: &msg,
				})
				return
			}
		}
		if roomID != subscribed && ev.Sender != mx.UserID {
			mx.mu.Lock()
			mx.unreads[roomID]++
			mx.mu.Unlock()
		}
		msg := mx.eventToMessage(ev)
		mx.events.Publish(backend.Event{
			Kind: backend.EventMessage, Chat: chat, DM: mx.isDirect(roomID), Message: &msg,
		})

	case "m.room.encrypted":
		// Never marked DM: agents cannot read encrypted content.
		msg := mx.eventToMessage(ev)
		mx.events.Publish(backend.Event{Kind: backend.EventMessage, Chat: chat, Message: &msg})

	case "m.room.redaction":
		targetID := ev.Redacts
		if targetID == "" {
			if tid, ok := ev.Content["redacts"].(string); ok {
				targetID = tid
			}
		}
		if targetID != "" {
			mx.events.Publish(backend.Event{
				Kind: backend.EventDelete, Chat: chat, Deleted: []string{targetID},
			})
		}

	case "m.reaction":
		relatesTo := mxRelatesTo(ev.Content)
		if relatesTo == nil {
			return
		}
		targetID, _ := relatesTo["event_id"].(string)
		emoji, _ := relatesTo["key"].(string)
		if targetID == "" || emoji == "" {
			return
		}
		if ev.Sender == mx.UserID {
			mx.mu.Lock()
			mx.ownReactions[targetID+"\x00"+emoji] = ev.EventID
			mx.mu.Unlock()
		}
		// Matrix has no reaction aggregation here, so this only tells the UI
		// which message changed.
		mx.events.Publish(backend.Event{
			Kind: backend.EventReaction, Chat: chat,
			Reaction: &backend.GenericReactionUpdate{MessageID: targetID},
		})
	}
}

func (mx *MxBackend) processTypingEvent(roomID string, ev mxTimelineEvent, subscribed string) {
	userIDs, ok := ev.Content["user_ids"].([]any)
	if !ok {
		return
	}
	if roomID == subscribed {
		mx.mu.Lock()
		mx.typing = make(map[string]time.Time)
		for _, uid := range userIDs {
			if s, ok := uid.(string); ok && s != mx.UserID {
				mx.typing[s] = time.Now()
			}
		}
		mx.mu.Unlock()
	}
	mx.events.Publish(backend.Event{
		Kind: backend.EventTyping, Chat: backend.ChatID{Backend: "matrix", Id: roomID},
	})
}

func (mx *MxBackend) loadSyncToken() {
	if mx.Store == nil {
		return
	}
	data, err := mx.Store.Get(mx.StoreKey, store.SyncToken)
	if err != nil {
		return
	}
	mx.syncToken = strings.TrimSpace(string(data))
}

func (mx *MxBackend) saveSyncToken() {
	if mx.Store == nil {
		return
	}
	mx.Store.Put(mx.StoreKey, store.SyncToken, []byte(mx.syncToken))
}

// Room display helpers.

func (mx *MxBackend) roomDisplayName(roomID string) string {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	room := mx.rooms[roomID]
	if room == nil {
		return roomID
	}
	if room.name != "" {
		return room.name
	}
	// For DMs or unnamed rooms, use member names.
	var others []string
	for uid, name := range room.members {
		if uid == mx.UserID {
			continue
		}
		if name != "" {
			others = append(others, name)
		} else {
			others = append(others, uid)
		}
	}
	if len(others) == 0 {
		return "Empty Room"
	}
	sort.Strings(others)
	if len(others) <= 3 {
		return strings.Join(others, ", ")
	}
	return fmt.Sprintf("%s and %d others", strings.Join(others[:2], ", "), len(others)-2)
}

func (mx *MxBackend) displayName(userID string) string {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if name, ok := mx.userCache[userID]; ok && name != "" {
		return name
	}
	// Strip the @ prefix and domain for a short form.
	if strings.HasPrefix(userID, "@") {
		if i := strings.Index(userID, ":"); i > 0 {
			return userID[1:i]
		}
	}
	return userID
}

// isDirect reports whether a room is a one-to-one DM, as Dialogs marks it.
func (mx *MxBackend) isDirect(roomID string) bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	room := mx.rooms[roomID]
	return mx.dmRooms[roomID] && room != nil && len(room.members) <= 2
}

// Dialogs.

func (mx *MxBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}
	mx.mu.Lock()
	type roomEntry struct {
		Id   string
		Room *mxRoom
	}
	var entries []roomEntry
	for id, room := range mx.rooms {
		entries = append(entries, roomEntry{id, room})
	}
	mx.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Room.lastAt.After(entries[j].Room.lastAt)
	})
	if len(entries) > 20 {
		entries = entries[:20]
	}

	result := make([]backend.ChatEntry, 0, len(entries))
	for _, e := range entries {
		mx.mu.Lock()
		isDM := mx.dmRooms[e.Id]
		unread := mx.unreads[e.Id]
		mx.mu.Unlock()
		memberCount := len(e.Room.members)
		result = append(result, backend.ChatEntry{
			ID:      backend.ChatID{Backend: "matrix", Id: e.Id},
			Title:   mx.roomDisplayName(e.Id),
			Unread:  unread,
			LastAt:  e.Room.lastAt,
			IsDM:    isDM && memberCount <= 2,
			IsGroup: !isDM || memberCount > 2,
		})
	}
	return result, 0, nil
}

// History.

func (mx *MxBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	roomID := ch.ID.Id
	mx.mu.Lock()
	room := mx.rooms[roomID]
	from := ""
	if room != nil {
		from = room.prevBatch
	}
	mx.mu.Unlock()
	if from == "" {
		from = mx.syncToken
	}
	msgs, end, err := mx.fetchMessages(ctx, roomID, from, 25)
	if err != nil {
		return nil, err
	}
	mx.mu.Lock()
	if room := mx.rooms[roomID]; room != nil {
		room.paginationEnd = end
	}
	mx.mu.Unlock()
	return msgs, nil
}

// UnreadMessages pages back from the latest sync. Reactions and state events
// share the timeline, so it reads a full page rather than just limit events.
func (mx *MxBackend) UnreadMessages(ctx context.Context, ch backend.ChatEntry, limit int) ([]backend.Message, error) {
	msgs, _, err := mx.fetchMessages(ctx, ch.ID.Id, mx.syncToken, 25)
	if err != nil {
		return nil, err
	}
	return backend.NewestIncoming(msgs, limit), nil
}

func (mx *MxBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	roomID := ch.ID.Id
	mx.mu.Lock()
	from := ""
	if room := mx.rooms[roomID]; room != nil {
		from = room.paginationEnd
	}
	mx.mu.Unlock()
	if from == "" {
		return nil, nil
	}
	msgs, end, err := mx.fetchMessages(ctx, roomID, from, 25)
	if err != nil {
		return nil, err
	}
	mx.mu.Lock()
	if room := mx.rooms[roomID]; room != nil {
		room.paginationEnd = end
	}
	mx.mu.Unlock()
	return msgs, nil
}

func (mx *MxBackend) Messages(ctx context.Context, ch backend.ChatEntry, ids []string) ([]backend.Message, error) {
	var msgs []backend.Message
	for _, id := range ids {
		var ev mxTimelineEvent
		path := fmt.Sprintf("/rooms/%s/event/%s", url.PathEscape(ch.ID.Id), url.PathEscape(id))
		if err := mx.apiJSON(ctx, "GET", path, nil, &ev); err != nil {
			// A redacted or invisible event must not hide the others.
			continue
		}
		if ev.Type == "m.room.message" || ev.Type == "m.room.encrypted" {
			msgs = append(msgs, mx.eventToMessage(ev))
		}
	}
	return msgs, nil
}

func (mx *MxBackend) fetchMessages(ctx context.Context, roomID, from string, limit int) ([]backend.Message, string, error) {
	path := fmt.Sprintf("/rooms/%s/messages?dir=b&limit=%d", url.PathEscape(roomID), limit)
	if from != "" {
		path += "&from=" + url.QueryEscape(from)
	}
	var resp mxMessagesResponse
	if err := mx.apiJSON(ctx, "GET", path, nil, &resp); err != nil {
		return nil, "", fmt.Errorf("load messages: %w", err)
	}
	for _, ev := range resp.State {
		if ev.StateKey != nil {
			mx.processStateEvent(roomID, ev)
		}
	}
	// Chunk comes newest-first; reverse for chronological order.
	msgs := make([]backend.Message, 0, len(resp.Chunk))
	for i := len(resp.Chunk) - 1; i >= 0; i-- {
		ev := resp.Chunk[i]
		if ev.Type == "m.room.message" || ev.Type == "m.room.encrypted" {
			// Skip edit events in history; the server should bundle them.
			if ev.Type == "m.room.message" {
				if rel := mxRelatesTo(ev.Content); rel != nil {
					if rt, _ := rel["rel_type"].(string); rt == "m.replace" {
						continue
					}
				}
			}
			msgs = append(msgs, mx.eventToMessage(ev))
		}
	}
	return msgs, resp.End, nil
}

func (mx *MxBackend) eventToMessage(ev mxTimelineEvent) backend.Message {
	outgoing := ev.Sender == mx.UserID
	sender := ""
	if !outgoing {
		sender = mx.displayName(ev.Sender)
	}

	text := ""
	mediaLabel := ""
	multiline := false
	image := false

	if ev.Type == "m.room.encrypted" {
		mediaLabel = "[encrypted message]"
	} else {
		msgtype, _ := ev.Content["msgtype"].(string)
		body, _ := ev.Content["body"].(string)

		switch msgtype {
		case "m.text", "m.notice":
			text = body
			multiline = strings.Contains(body, "\n")
		case "m.emote":
			text = "* " + body
		case "m.image":
			mediaLabel = mxMediaLabel("photo", ev.Content)
			image = mxInlineImage(ev.Content)
		case "m.video":
			mediaLabel = mxMediaLabel("video", ev.Content)
		case "m.audio":
			mediaLabel = mxMediaLabel("audio", ev.Content)
		case "m.file":
			mediaLabel = mxMediaLabel("file", ev.Content)
		default:
			text = body
		}
	}

	var editDate int
	if ev.Type == "m.room.message" {
		if rel := mxRelatesTo(ev.Content); rel != nil {
			if rt, _ := rel["rel_type"].(string); rt == "m.replace" {
				editDate = int(ev.OriginServerTS / 1000)
			}
		}
	}

	replyToID := ""
	if rel := mxRelatesTo(ev.Content); rel != nil {
		if inReplyTo, ok := rel["m.in_reply_to"].(map[string]any); ok {
			if eid, ok := inReplyTo["event_id"].(string); ok {
				replyToID = eid
			}
		}
	}
	// Strip Matrix reply fallback from body.
	if replyToID != "" && strings.HasPrefix(text, "> ") {
		if idx := strings.Index(text, "\n\n"); idx >= 0 {
			text = text[idx+2:]
		}
	}

	return backend.Message{
		ID:         ev.EventID,
		Sender:     sender,
		SenderID:   ev.Sender,
		Text:       text,
		Timestamp:  time.UnixMilli(ev.OriginServerTS).Local(),
		Outgoing:   outgoing,
		EditDate:   editDate,
		ReplyToID:  replyToID,
		MediaLabel: mediaLabel,
		Image:      image,
		Multiline:  multiline,
		Raw:        ev,
	}
}

// mxInlineImage reports whether an m.image can be fetched unencrypted and is
// small enough to draw inline; a missing size is trusted.
func mxInlineImage(content map[string]any) bool {
	if url, _ := content["url"].(string); url == "" {
		return false
	}
	info, _ := content["info"].(map[string]any)
	size, _ := info["size"].(float64)
	return size <= backend.MaxInlineImage
}

func mxMediaLabel(kind string, content map[string]any) string {
	info, _ := content["info"].(map[string]any)
	if info == nil {
		body, _ := content["body"].(string)
		if body != "" {
			return fmt.Sprintf("[%s] %s", kind, body)
		}
		return fmt.Sprintf("[%s]", kind)
	}
	size, _ := info["size"].(float64)
	w, _ := info["w"].(float64)
	h, _ := info["h"].(float64)
	if kind == "photo" && w > 0 && h > 0 {
		return fmt.Sprintf("[%s] %dx%d, %s", kind, int(w), int(h), textfmt.FormatBytes(int64(size)))
	}
	body, _ := content["body"].(string)
	if body != "" {
		return fmt.Sprintf("[%s] %s, %s", kind, body, textfmt.FormatBytes(int64(size)))
	}
	return fmt.Sprintf("[%s] %s", kind, textfmt.FormatBytes(int64(size)))
}

// Send.

func (mx *MxBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	txnID := mx.nextTxnID()
	content := map[string]any{
		"msgtype": "m.text",
		"body":    text,
	}
	if replyTo != "" {
		content["m.relates_to"] = map[string]any{
			"m.in_reply_to": map[string]string{
				"event_id": replyTo,
			},
		}
	}
	path := fmt.Sprintf("/rooms/%s/send/m.room.message/%s", url.PathEscape(ch.ID.Id), url.PathEscape(txnID))
	var resp struct {
		EventID string `json:"event_id"`
	}
	if err := mx.apiJSON(ctx, "PUT", path, content, &resp); err != nil {
		return backend.Message{}, fmt.Errorf("send message: %w", err)
	}
	return backend.Message{
		ID:        resp.EventID,
		Text:      text,
		Timestamp: time.Now(),
		Outgoing:  true,
		ReplyToID: replyTo,
	}, nil
}

func (mx *MxBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, filePath, caption, replyTo string) (backend.Message, error) {
	mxcURI, filename, mimeType, size, err := mx.uploadFile(ctx, filePath)
	if err != nil {
		return backend.Message{}, err
	}

	msgtype := "m.file"
	mediaKind := "file"
	if strings.HasPrefix(mimeType, "image/") {
		msgtype = "m.image"
		mediaKind = "photo"
	} else if strings.HasPrefix(mimeType, "video/") {
		msgtype = "m.video"
		mediaKind = "video"
	} else if strings.HasPrefix(mimeType, "audio/") {
		msgtype = "m.audio"
		mediaKind = "audio"
	}

	content := map[string]any{
		"msgtype": msgtype,
		"body":    filename,
		"url":     mxcURI,
		"info": map[string]any{
			"size":     size,
			"mimetype": mimeType,
		},
	}
	if caption != "" {
		content["body"] = caption
		content["filename"] = filename
	}
	if replyTo != "" {
		content["m.relates_to"] = map[string]any{
			"m.in_reply_to": map[string]string{
				"event_id": replyTo,
			},
		}
	}

	txnID := mx.nextTxnID()
	path := fmt.Sprintf("/rooms/%s/send/m.room.message/%s", url.PathEscape(ch.ID.Id), url.PathEscape(txnID))
	var resp struct {
		EventID string `json:"event_id"`
	}
	if err := mx.apiJSON(ctx, "PUT", path, content, &resp); err != nil {
		return backend.Message{}, fmt.Errorf("send file: %w", err)
	}
	label := fmt.Sprintf("[%s] %s, %s", mediaKind, filename, textfmt.FormatBytes(size))
	return backend.Message{
		ID:         resp.EventID,
		Timestamp:  time.Now(),
		Outgoing:   true,
		MediaLabel: label,
		Text:       caption,
		ReplyToID:  replyTo,
	}, nil
}

func (mx *MxBackend) uploadFile(ctx context.Context, filePath string) (mxcURI, filename, mimeType string, size int64, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", "", "", 0, fmt.Errorf("stat file: %w", err)
	}
	size = stat.Size()
	filename = filepath.Base(filePath)
	mimeType = mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	uploadURL := mx.Homeserver + "/_matrix/media/v3/upload?filename=" + url.QueryEscape(filename)
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, f)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Authorization", "Bearer "+mx.AccessToken)
	req.ContentLength = size

	resp, err := mx.client.Do(req)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("upload file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", "", 0, fmt.Errorf("upload failed: %s", string(data))
	}
	var result struct {
		ContentURI string `json:"content_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", "", 0, fmt.Errorf("decode upload response: %w", err)
	}
	return result.ContentURI, filename, mimeType, size, nil
}

func (mx *MxBackend) UploadPlaceholder(path, caption string) backend.Message {
	return backend.Message{
		Outgoing:   true,
		Timestamp:  time.Now(),
		MediaLabel: "[uploading]",
		Text:       caption,
	}
}

// Edit.

func (mx *MxBackend) Editable(msg backend.Message) error {
	return backend.GenericEditable(msg)
}

func (mx *MxBackend) Edit(ctx context.Context, ch backend.ChatEntry, msg backend.Message, text string) error {
	txnID := mx.nextTxnID()
	content := map[string]any{
		"msgtype": "m.text",
		"body":    "* " + text,
		"m.new_content": map[string]any{
			"msgtype": "m.text",
			"body":    text,
		},
		"m.relates_to": map[string]any{
			"rel_type": "m.replace",
			"event_id": msg.ID,
		},
	}
	path := fmt.Sprintf("/rooms/%s/send/m.room.message/%s", url.PathEscape(ch.ID.Id), url.PathEscape(txnID))
	return mx.apiJSON(ctx, "PUT", path, content, nil)
}

// Delete (redact).

func (mx *MxBackend) DeleteMessages(ctx context.Context, ch backend.ChatEntry, ids []string, forEveryone bool) error {
	for _, id := range ids {
		txnID := mx.nextTxnID()
		path := fmt.Sprintf("/rooms/%s/redact/%s/%s", url.PathEscape(ch.ID.Id), url.PathEscape(id), url.PathEscape(txnID))
		if err := mx.apiJSON(ctx, "PUT", path, map[string]string{}, nil); err != nil {
			return fmt.Errorf("redact message: %w", err)
		}
	}
	return nil
}

func (mx *MxBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported on Matrix")
}

// Reactions.

func (mx *MxBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
	key := msg.ID + "\x00" + emoji
	mx.mu.Lock()
	existingID := mx.ownReactions[key]
	mx.mu.Unlock()

	if existingID != "" {
		// Remove reaction by redacting the reaction event.
		txnID := mx.nextTxnID()
		path := fmt.Sprintf("/rooms/%s/redact/%s/%s", url.PathEscape(ch.ID.Id), url.PathEscape(existingID), url.PathEscape(txnID))
		if err := mx.apiJSON(ctx, "PUT", path, map[string]string{}, nil); err != nil {
			return nil, fmt.Errorf("remove reaction: %w", err)
		}
		mx.mu.Lock()
		delete(mx.ownReactions, key)
		mx.mu.Unlock()
	} else {
		// Add reaction.
		txnID := mx.nextTxnID()
		content := map[string]any{
			"m.relates_to": map[string]any{
				"rel_type": "m.annotation",
				"event_id": msg.ID,
				"key":      emoji,
			},
		}
		path := fmt.Sprintf("/rooms/%s/send/m.reaction/%s", url.PathEscape(ch.ID.Id), url.PathEscape(txnID))
		var resp struct {
			EventID string `json:"event_id"`
		}
		if err := mx.apiJSON(ctx, "PUT", path, content, &resp); err != nil {
			return nil, fmt.Errorf("add reaction: %w", err)
		}
		mx.mu.Lock()
		mx.ownReactions[key] = resp.EventID
		mx.mu.Unlock()
	}

	// Return updated reaction list from existing message reactions, toggled.
	updated := make([]backend.MessageReaction, 0, len(msg.Reactions))
	found := false
	for _, r := range msg.Reactions {
		nr := r
		if r.Emoji == emoji {
			found = true
			if existingID != "" {
				nr.Count--
				nr.Chosen = false
			} else {
				nr.Count++
				nr.Chosen = true
			}
			if nr.Count > 0 {
				updated = append(updated, nr)
			}
		} else {
			updated = append(updated, nr)
		}
	}
	if !found && existingID == "" {
		updated = append(updated, backend.MessageReaction{Emoji: emoji, Count: 1, Chosen: true})
	}
	return updated, nil
}

// Read receipts.

func (mx *MxBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	path := fmt.Sprintf("/rooms/%s/receipt/m.read/%s", url.PathEscape(ch.ID.Id), url.PathEscape(upToID))
	if err := mx.apiJSON(ctx, "POST", path, map[string]any{}, nil); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	mx.mu.Lock()
	mx.unreads[ch.ID.Id] = 0
	mx.mu.Unlock()
	return nil
}

// Typing.

func (mx *MxBackend) SetTyping(ctx context.Context, ch backend.ChatEntry) {
	path := fmt.Sprintf("/rooms/%s/typing/%s", url.PathEscape(ch.ID.Id), url.PathEscape(mx.UserID))
	mx.apiJSON(ctx, "PUT", path, map[string]any{"typing": true, "timeout": 5000}, nil)
}

// Forwarding and pinning (not supported).

func (mx *MxBackend) Forward(ctx context.Context, from backend.ChatEntry, to backend.ChatEntry, msgID string) error {
	return fmt.Errorf("forwarding is not supported on Matrix")
}

func (mx *MxBackend) Pin(ctx context.Context, ch backend.ChatEntry, msg backend.Message, unpin bool) error {
	return fmt.Errorf("pinning is not supported on Matrix")
}

// Search.

func (mx *MxBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
	body := map[string]any{
		"search_categories": map[string]any{
			"room_events": map[string]any{
				"search_term": query,
				"filter": map[string]any{
					"rooms": []string{ch.ID.Id},
				},
				"order_by": "recent",
			},
		},
	}
	var resp struct {
		SearchCategories struct {
			RoomEvents struct {
				Results []struct {
					Result mxTimelineEvent `json:"result"`
				} `json:"results"`
			} `json:"room_events"`
		} `json:"search_categories"`
	}
	if err := mx.apiJSON(ctx, "POST", "/search", body, &resp); err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	messages := make([]backend.Message, 0, len(resp.SearchCategories.RoomEvents.Results))
	for _, r := range resp.SearchCategories.RoomEvents.Results {
		messages = append(messages, mx.eventToMessage(r.Result))
	}
	return messages, nil
}

// Real-time events.

func (mx *MxBackend) SetActive(ch backend.ChatEntry) {
	mx.mu.Lock()
	mx.subscribed = ch.ID.Id
	mx.mu.Unlock()
}

func (mx *MxBackend) Events() *backend.Bus { return &mx.events }

func (mx *MxBackend) IsTyping() bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	for _, t := range mx.typing {
		if time.Since(t) < 6*time.Second {
			return true
		}
	}
	return false
}

func (mx *MxBackend) ClearTyping() {
	mx.mu.Lock()
	mx.typing = make(map[string]time.Time)
	mx.mu.Unlock()
}

// Read state.

func (mx *MxBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string { return "" }

func (mx *MxBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error { return nil }

func (mx *MxBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	mx.mu.Lock()
	n := mx.unreads[ch.ID.Id]
	mx.mu.Unlock()
	return n, nil
}

func (mx *MxBackend) CachedUnread(ch backend.ChatEntry) int {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return mx.unreads[ch.ID.Id]
}

func (mx *MxBackend) InboxPosition(ch backend.ChatEntry) string { return "" }

// Media.

func (mx *MxBackend) MediaPlayable(msg backend.Message) bool {
	ev, ok := msg.Raw.(mxTimelineEvent)
	if !ok {
		return false
	}
	mxcURL, _ := ev.Content["url"].(string)
	if mxcURL == "" {
		return false
	}
	msgtype, _ := ev.Content["msgtype"].(string)
	return msgtype == "m.image" || msgtype == "m.video" || msgtype == "m.audio"
}

func (mx *MxBackend) MediaExec(ctx context.Context, msg backend.Message, player string) (*exec.Cmd, func(), error) {
	ev, ok := msg.Raw.(mxTimelineEvent)
	if !ok {
		return nil, nil, fmt.Errorf("this message has no playable media")
	}
	mxcURL, _ := ev.Content["url"].(string)
	if mxcURL == "" {
		return nil, nil, fmt.Errorf("this message has no playable media")
	}
	body, _ := ev.Content["body"].(string)
	if body == "" {
		body = "media"
	}
	tmpDir, err := os.MkdirTemp("", "wiregram-mx-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }
	dest := filepath.Join(tmpDir, filepath.Base(body))
	if err := mx.downloadMXC(ctx, mxcURL, dest); err != nil {
		cleanup()
		return nil, nil, err
	}
	msgtype, _ := ev.Content["msgtype"].(string)
	audio := msgtype == "m.audio"
	return backend.MpvCmd(ctx, player, audio, dest), cleanup, nil
}

func (mx *MxBackend) MediaSaveable(msg backend.Message) (string, bool) {
	ev, ok := msg.Raw.(mxTimelineEvent)
	if !ok {
		return "", false
	}
	mxcURL, _ := ev.Content["url"].(string)
	if mxcURL == "" {
		return "", false
	}
	body, _ := ev.Content["body"].(string)
	if body == "" {
		body = "attachment"
	}
	return body, true
}

func (mx *MxBackend) SaveMedia(ctx context.Context, msg backend.Message, destDir string) error {
	ev, ok := msg.Raw.(mxTimelineEvent)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	mxcURL, _ := ev.Content["url"].(string)
	if mxcURL == "" {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	body, _ := ev.Content["body"].(string)
	if body == "" {
		body = "attachment"
	}
	return mx.downloadMXC(ctx, mxcURL, filepath.Join(destDir, body))
}

func (mx *MxBackend) downloadMXC(ctx context.Context, mxcURL, dest string) error {
	body, err := mx.openMXC(ctx, mxcURL)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if err := backend.FirstError(copyErr, closeErr); err != nil {
		return fmt.Errorf("save file: %w", err)
	}
	return nil
}

// openMXC starts an authenticated download of an mxc:// URL.
func (mx *MxBackend) openMXC(ctx context.Context, mxcURL string) (io.ReadCloser, error) {
	// Parse mxc://server/mediaId
	if !strings.HasPrefix(mxcURL, "mxc://") {
		return nil, fmt.Errorf("invalid mxc URL: %s", mxcURL)
	}
	parts := strings.SplitN(mxcURL[6:], "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid mxc URL: %s", mxcURL)
	}
	// Escape both parts: mxc URLs come from remote events, and a raw "../"
	// would otherwise reach other homeserver endpoints with our token.
	downloadURL := mx.Homeserver + "/_matrix/media/v3/download/" +
		url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+mx.AccessToken)
	resp, err := mx.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// LinkPreview asks the homeserver to summarise link; wiregram never fetches
// the page itself. Older servers only have the unauthenticated media path.
func (mx *MxBackend) LinkPreview(ctx context.Context, link string) (*backend.LinkPreview, error) {
	paths := []string{"/_matrix/client/v1/media/preview_url", "/_matrix/media/v3/preview_url"}
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, "GET",
			mx.Homeserver+path+"?url="+url.QueryEscape(link), nil)
		if err != nil {
			return nil, fmt.Errorf("create preview request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+mx.AccessToken)
		resp, err := mx.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("link preview: %w", err)
		}
		if resp.StatusCode == http.StatusNotFound && path == paths[0] {
			resp.Body.Close()
			continue
		}
		var og struct {
			Site        string `json:"og:site_name"`
			Title       string `json:"og:title"`
			Description string `json:"og:description"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&og)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("link preview: HTTP %d", resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("decode link preview: %w", err)
		}
		if og.Title == "" && og.Description == "" {
			return nil, nil
		}
		site := og.Site
		if u, err := url.Parse(link); site == "" && err == nil {
			site = u.Hostname()
		}
		return &backend.LinkPreview{
			URL: link, Site: site, Title: og.Title, Description: og.Description,
		}, nil
	}
	return nil, nil
}

func (mx *MxBackend) AcknowledgeMedia(ctx context.Context, msg backend.Message) {}

// User search.

func (mx *MxBackend) UserSearchBase(ctx context.Context, recentChats []backend.ChatEntry) (backend.UserSearchBase, error) {
	var base []backend.UserCandidate
	for _, ch := range recentChats {
		if !ch.IsDM {
			continue
		}
		mx.mu.Lock()
		room := mx.rooms[ch.ID.Id]
		var members map[string]string
		if room != nil {
			members = make(map[string]string, len(room.members))
			for uid, name := range room.members {
				members[uid] = name
			}
		}
		mx.mu.Unlock()
		for uid, name := range members {
			if uid == mx.UserID {
				continue
			}
			if name == "" {
				name = uid
			}
			base = append(base, backend.UserCandidate{Key: uid, Label: name,
				Match: strings.ToLower(name + " " + uid), Raw: uid})
		}
	}
	return backend.UserSearchBase{Heading: "Recent DMs", Base: base, Local: base}, nil
}

func (mx *MxBackend) UserSearchRemote(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	var resp struct {
		Results []struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
		} `json:"results"`
	}
	if err := mx.apiJSON(ctx, "POST", "/user_directory/search",
		map[string]any{"search_term": query, "limit": 20}, &resp); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	var out []backend.UserCandidate
	for _, u := range resp.Results {
		if u.UserID == mx.UserID {
			continue
		}
		name := u.DisplayName
		if name == "" {
			name = u.UserID
		}
		out = append(out, backend.UserCandidate{Key: u.UserID, Label: name + "  " + u.UserID,
			Match: strings.ToLower(name + " " + u.UserID), Raw: u.UserID})
	}
	return out, nil
}

func (mx *MxBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	userID, ok := c.Raw.(string)
	if !ok {
		return backend.ChatEntry{}, fmt.Errorf("open chat: unexpected search result")
	}
	return mx.openDM(ctx, userID)
}

func (mx *MxBackend) openDM(ctx context.Context, userID string) (backend.ChatEntry, error) {
	// Check if we already have a DM room with this user.
	mx.mu.Lock()
	for roomID, room := range mx.rooms {
		if !mx.dmRooms[roomID] {
			continue
		}
		if _, ok := room.members[userID]; ok && len(room.members) <= 2 {
			mx.mu.Unlock()
			return backend.ChatEntry{
				ID:    backend.ChatID{Backend: "matrix", Id: roomID},
				Title: mx.displayName(userID),
				IsDM:  true,
			}, nil
		}
	}
	mx.mu.Unlock()

	var resp struct {
		RoomID string `json:"room_id"`
	}
	body := map[string]any{
		"is_direct": true,
		"invite":    []string{userID},
		"preset":    "trusted_private_chat",
	}
	if err := mx.apiJSON(ctx, "POST", "/createRoom", body, &resp); err != nil {
		return backend.ChatEntry{}, fmt.Errorf("create DM with %s: %w", userID, err)
	}
	mx.mu.Lock()
	mx.dmRooms[resp.RoomID] = true
	mx.rooms[resp.RoomID] = &mxRoom{
		members: map[string]string{mx.UserID: "", userID: mx.displayName(userID)},
		lastAt:  time.Now(),
	}
	mx.mu.Unlock()
	return backend.ChatEntry{
		ID:    backend.ChatID{Backend: "matrix", Id: resp.RoomID},
		Title: mx.displayName(userID),
		IsDM:  true,
	}, nil
}

// Emoji.

func (mx *MxBackend) RecentEmoji() []string { return mx.recentEmoji }

func (mx *MxBackend) RememberEmoji(value string) {
	for i, v := range mx.recentEmoji {
		if v == value {
			mx.recentEmoji = append(mx.recentEmoji[:i], mx.recentEmoji[i+1:]...)
			break
		}
	}
	mx.recentEmoji = append([]string{value}, mx.recentEmoji...)
	if len(mx.recentEmoji) > 20 {
		mx.recentEmoji = mx.recentEmoji[:20]
	}
}

func (mx *MxBackend) EmojiChoices(query string) []backend.EmojiOption {
	catalog := backend.EmojiCatalog()
	if query == "" {
		return backend.RecentFirst(catalog, mx.recentEmoji)
	}
	q := strings.ToLower(query)
	var matches []backend.EmojiOption
	for _, e := range catalog {
		if strings.Contains(strings.ToLower(e.Name), q) || strings.Contains(e.Value, q) {
			matches = append(matches, e)
			if len(matches) >= 10 {
				break
			}
		}
	}
	return matches
}

func (mx *MxBackend) ReactionChoices(query string) []backend.EmojiOption {
	return mx.EmojiChoices(query)
}

// Content helpers.

func mxRelatesTo(content map[string]any) map[string]any {
	rel, ok := content["m.relates_to"].(map[string]any)
	if !ok {
		return nil
	}
	return rel
}
