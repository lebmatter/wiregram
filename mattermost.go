package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type mmBackend struct {
	serverURL string
	token     string
	userID    string
	username  string

	mu          sync.Mutex
	subscribed  string // channel ID
	pending     []mmEvent
	changed     chan struct{}
	typing      map[string]time.Time // userID → last typing event
	unreads     map[string]int       // channelID → unread count
	lastRead    map[string]string    // channelID → last read post ID
	userCache   map[string]mmUser
	recentEmoji []string

	client *http.Client
	wsConn *websocket.Conn
	wsStop context.CancelFunc
}

type mmUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
}

func (u mmUser) displayName() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	if u.First != "" {
		name := u.First
		if u.Last != "" {
			name += " " + u.Last
		}
		return name
	}
	return u.Username
}

type mmPost struct {
	ID        string          `json:"id"`
	ChannelID string          `json:"channel_id"`
	UserID    string          `json:"user_id"`
	Message   string          `json:"message"`
	CreateAt  int64           `json:"create_at"`
	UpdateAt  int64           `json:"update_at"`
	EditAt    int64           `json:"edit_at"`
	DeleteAt  int64           `json:"delete_at"`
	IsPinned  bool            `json:"is_pinned"`
	RootID    string          `json:"root_id"`
	FileIDs   []string        `json:"file_ids"`
	Metadata  *mmPostMetadata `json:"metadata,omitempty"`
}

type mmPostMetadata struct {
	Files []mmFileInfo `json:"files,omitempty"`
}

type mmFileInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Extension string `json:"extension"`
}

type mmChannel struct {
	ID            string `json:"id"`
	Type          string `json:"type"` // O=public, P=private, D=DM, G=group
	DisplayName   string `json:"display_name"`
	Name          string `json:"name"`
	TeamID        string `json:"team_id"`
	LastPostAt    int64  `json:"last_post_at"`
	TotalMsgCount int64  `json:"total_msg_count"`
}

type mmTeam struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type mmChannelMember struct {
	ChannelID    string `json:"channel_id"`
	MsgCount     int64  `json:"msg_count"`
	MentionCount int    `json:"mention_count"`
}

type mmEvent struct {
	kind string // "post", "edit", "delete", "reaction", "typing"
	post *mmPost
	data json.RawMessage
}

type mmReaction struct {
	UserID    string `json:"user_id"`
	PostID    string `json:"post_id"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at"`
}

func newMMBackend(serverURL string) *mmBackend {
	return &mmBackend{
		serverURL: strings.TrimRight(serverURL, "/"),
		changed:   make(chan struct{}, 1),
		typing:    make(map[string]time.Time),
		unreads:   make(map[string]int),
		lastRead:  make(map[string]string),
		userCache: make(map[string]mmUser),
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (mb *mmBackend) Name() string { return "mattermost" }

func (mb *mmBackend) Capabilities() capabilities {
	return capabilities{
		Forward:       false,
		DeleteForMe:   false,
		OutboxReceipt: false,
		ViewOnce:      false,
		Reactions:     true,
		Edit:          true,
		Pin:           true,
		Upload:        true,
		Search:        true,
		Typing:        true,
		DeleteChat:    false,
	}
}

func (mb *mmBackend) api(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, mb.serverURL+"/api/v4"+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mb.token != "" {
		req.Header.Set("Authorization", "Bearer "+mb.token)
	}
	return mb.client.Do(req)
}

func (mb *mmBackend) apiJSON(ctx context.Context, method, path string, body, result any) error {
	resp, err := mb.api(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("Mattermost API %s: %s", path, string(data))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (mb *mmBackend) Run(ctx context.Context) error {
	mb.startWebSocket(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (mb *mmBackend) Authenticate(ctx context.Context, prompt promptFunc, report func(error)) error {
	if mb.token != "" {
		var me mmUser
		if err := mb.apiJSON(ctx, "GET", "/users/me", nil, &me); err == nil {
			mb.userID = me.ID
			mb.username = me.Username
			mb.mu.Lock()
			mb.userCache[me.ID] = me
			mb.mu.Unlock()
			return nil
		}
		mb.token = ""
	}

	email, err := prompt("Email or username: ", false)
	if err != nil {
		return fmt.Errorf("read email: %w", err)
	}
	password, err := prompt("Password: ", true)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	resp, err := mb.api(ctx, "POST", "/users/login", map[string]string{
		"login_id": strings.TrimSpace(email),
		"password": strings.TrimSpace(password),
	})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("login failed: %s", string(data))
	}
	mb.token = resp.Header.Get("Token")
	var me mmUser
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	mb.userID = me.ID
	mb.username = me.Username
	mb.mu.Lock()
	mb.userCache[me.ID] = me
	mb.mu.Unlock()
	return nil
}

func (mb *mmBackend) Identity() accountIdentity {
	return accountIdentity{Name: mb.username}
}

func (mb *mmBackend) AccountLabel() string {
	host := mb.serverURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	return fmt.Sprintf("[MM] %s@%s", mb.username, host)
}

func (mb *mmBackend) AutoLockEnabled() bool { return false }

func (mb *mmBackend) Dialogs(ctx context.Context, archived bool) ([]chatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}

	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil {
		return nil, 0, fmt.Errorf("load teams: %w", err)
	}

	type channelWithUnread struct {
		ch       mmChannel
		unread   int
		teamName string
	}
	teamNames := make(map[string]string, len(teams))
	for _, team := range teams {
		teamNames[team.ID] = team.DisplayName
	}

	var allChannels []mmChannel
	page := 0
	for {
		var batch []mmChannel
		if err := mb.apiJSON(ctx, "GET", fmt.Sprintf("/users/me/channels?page=%d&per_page=100", page), nil, &batch); err != nil {
			return nil, 0, fmt.Errorf("load channels: %w", err)
		}
		allChannels = append(allChannels, batch...)
		if len(batch) < 100 {
			break
		}
		page++
	}

	memberMap := make(map[string]mmChannelMember)
	for _, team := range teams {
		var members []mmChannelMember
		if err := mb.apiJSON(ctx, "GET", fmt.Sprintf("/users/me/teams/%s/channels/members", team.ID), nil, &members); err != nil {
			return nil, 0, fmt.Errorf("load members: %w", err)
		}
		for _, m := range members {
			memberMap[m.ChannelID] = m
		}
	}

	seen := make(map[string]bool)
	var channels []channelWithUnread
	for _, ch := range allChannels {
		if seen[ch.ID] {
			continue
		}
		seen[ch.ID] = true
		unread := 0
		if m, ok := memberMap[ch.ID]; ok {
			unread = int(ch.TotalMsgCount - m.MsgCount)
			if unread < 0 {
				unread = 0
			}
		}
		channels = append(channels, channelWithUnread{ch, unread, teamNames[ch.TeamID]})
	}

	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ch.LastPostAt > channels[j].ch.LastPostAt
	})

	if len(channels) > 20 {
		channels = channels[:20]
	}

	entries := make([]chatEntry, 0, len(channels))
	for _, cw := range channels {
		title := mb.channelTitle(ctx, cw.ch, cw.teamName)
		isDM := cw.ch.Type == "D" || cw.ch.Type == "G"
		mb.mu.Lock()
		mb.unreads[cw.ch.ID] = cw.unread
		mb.mu.Unlock()
		entries = append(entries, chatEntry{
			ID:      chatID{backend: "mattermost", id: cw.ch.ID},
			Title:   title,
			Unread:  cw.unread,
			IsDM:    isDM,
			IsGroup: !isDM,
			Raw:     cw.ch,
		})
	}
	return entries, 0, nil
}

func (mb *mmBackend) channelTitle(ctx context.Context, ch mmChannel, teamName string) string {
	switch ch.Type {
	case "D":
		parts := strings.Split(ch.Name, "__")
		for _, uid := range parts {
			if uid != mb.userID && uid != "" {
				u := mb.resolveUser(ctx, uid)
				return u.displayName()
			}
		}
		return "DM (self)"
	case "G":
		return ch.DisplayName
	default:
		name := ch.DisplayName
		if name == "" {
			name = ch.Name
		}
		if teamName != "" {
			return "[" + teamName + "] " + name
		}
		return name
	}
}

func (mb *mmBackend) resolveUser(ctx context.Context, userID string) mmUser {
	mb.mu.Lock()
	if u, ok := mb.userCache[userID]; ok {
		mb.mu.Unlock()
		return u
	}
	mb.mu.Unlock()

	var u mmUser
	if err := mb.apiJSON(ctx, "GET", "/users/"+userID, nil, &u); err != nil {
		return mmUser{ID: userID, Username: userID[:8]}
	}
	mb.mu.Lock()
	mb.userCache[userID] = u
	mb.mu.Unlock()
	return u
}

func (mb *mmBackend) postToMessage(ctx context.Context, p mmPost) message {
	sender := ""
	senderID := p.UserID
	outgoing := p.UserID == mb.userID
	if !outgoing {
		u := mb.resolveUser(ctx, p.UserID)
		sender = u.displayName()
	}

	var mediaLabel string
	if p.Metadata != nil {
		for _, f := range p.Metadata.Files {
			if mediaLabel != "" {
				mediaLabel += ", "
			}
			mediaLabel += mmFileLabel(f)
		}
	} else if len(p.FileIDs) > 0 {
		mediaLabel = fmt.Sprintf("[%d file(s)]", len(p.FileIDs))
	}

	var editDate int
	if p.EditAt > 0 {
		editDate = int(p.EditAt / 1000)
	}

	return message{
		ID:         p.ID,
		Sender:     sender,
		SenderID:   senderID,
		Text:       p.Message,
		Timestamp:  time.UnixMilli(p.CreateAt).Local(),
		Outgoing:   outgoing,
		Pinned:     p.IsPinned,
		EditDate:   editDate,
		ReplyToID:  p.RootID,
		MediaLabel: mediaLabel,
		Multiline:  strings.Contains(p.Message, "\n"),
		Raw:        p,
	}
}

func mmFileLabel(f mmFileInfo) string {
	kind := "file"
	if strings.HasPrefix(f.MimeType, "image/") {
		kind = "photo"
		if f.Width > 0 {
			return fmt.Sprintf("[%s] %dx%d, %s", kind, f.Width, f.Height, formatBytes(f.Size))
		}
	} else if strings.HasPrefix(f.MimeType, "video/") {
		kind = "video"
	} else if strings.HasPrefix(f.MimeType, "audio/") {
		kind = "audio"
	}
	return fmt.Sprintf("[%s] %s, %s", kind, f.Name, formatBytes(f.Size))
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KB", b>>10)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (mb *mmBackend) History(ctx context.Context, ch chatEntry) ([]message, error) {
	return mb.fetchPosts(ctx, ch.ID.id, "", 25)
}

func (mb *mmBackend) OlderHistory(ctx context.Context, ch chatEntry, beforeID string) ([]message, error) {
	return mb.fetchPosts(ctx, ch.ID.id, beforeID, 25)
}

func (mb *mmBackend) fetchPosts(ctx context.Context, channelID, before string, perPage int) ([]message, error) {
	path := fmt.Sprintf("/channels/%s/posts?per_page=%d", channelID, perPage)
	if before != "" {
		path += "&before=" + before
	}
	var result struct {
		Order []string          `json:"order"`
		Posts map[string]mmPost `json:"posts"`
	}
	if err := mb.apiJSON(ctx, "GET", path, nil, &result); err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	messages := make([]message, 0, len(result.Order))
	for i := len(result.Order) - 1; i >= 0; i-- {
		p, ok := result.Posts[result.Order[i]]
		if !ok || p.DeleteAt > 0 {
			continue
		}
		messages = append(messages, mb.postToMessage(ctx, p))
	}
	mb.loadReactions(ctx, messages)
	return messages, nil
}

func (mb *mmBackend) loadReactions(ctx context.Context, messages []message) {
	for i, msg := range messages {
		var reactions []mmReaction
		if err := mb.apiJSON(ctx, "GET", "/posts/"+msg.ID+"/reactions", nil, &reactions); err != nil {
			continue
		}
		messages[i].Reactions = mb.aggregateReactions(reactions)
	}
}

func (mb *mmBackend) aggregateReactions(reactions []mmReaction) []messageReaction {
	counts := make(map[string]*messageReaction)
	var order []string
	for _, r := range reactions {
		if _, ok := counts[r.EmojiName]; !ok {
			order = append(order, r.EmojiName)
			counts[r.EmojiName] = &messageReaction{Emoji: r.EmojiName}
		}
		mr := counts[r.EmojiName]
		mr.Count++
		if r.UserID == mb.userID {
			mr.Chosen = true
		}
	}
	result := make([]messageReaction, 0, len(order))
	for _, name := range order {
		result = append(result, *counts[name])
	}
	return result
}

func (mb *mmBackend) Send(ctx context.Context, ch chatEntry, text string, replyTo string) (message, error) {
	post := map[string]string{
		"channel_id": ch.ID.id,
		"message":    text,
	}
	if replyTo != "" {
		post["root_id"] = replyTo
	}
	var result mmPost
	if err := mb.apiJSON(ctx, "POST", "/posts", post, &result); err != nil {
		return message{}, fmt.Errorf("send message: %w", err)
	}
	return mb.postToMessage(ctx, result), nil
}

func (mb *mmBackend) SendUpload(ctx context.Context, ch chatEntry, path, caption, replyTo string) (message, error) {
	fileIDs, err := mb.uploadFile(ctx, ch.ID.id, path)
	if err != nil {
		return message{}, err
	}
	post := struct {
		ChannelID string   `json:"channel_id"`
		Message   string   `json:"message"`
		RootID    string   `json:"root_id,omitempty"`
		FileIDs   []string `json:"file_ids"`
	}{
		ChannelID: ch.ID.id,
		Message:   caption,
		RootID:    replyTo,
		FileIDs:   fileIDs,
	}
	var result mmPost
	if err := mb.apiJSON(ctx, "POST", "/posts", post, &result); err != nil {
		return message{}, fmt.Errorf("send file message: %w", err)
	}
	return mb.postToMessage(ctx, result), nil
}

func (mb *mmBackend) uploadFile(ctx context.Context, channelID, filePath string) ([]string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("channel_id", channelID); err != nil {
		return nil, fmt.Errorf("write channel_id: %w", err)
	}
	filename := filepath.Base(filePath)
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename=%q`, filename))
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, fmt.Errorf("create form part: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, fmt.Errorf("copy file data: %w", err)
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", mb.serverURL+"/api/v4/files", &buf)
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+mb.token)
	resp, err := mb.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("upload failed: %s", string(data))
	}
	var result struct {
		FileInfos []mmFileInfo `json:"file_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	ids := make([]string, len(result.FileInfos))
	for i, fi := range result.FileInfos {
		ids[i] = fi.ID
	}
	return ids, nil
}

func (mb *mmBackend) UploadPlaceholder(path, caption string) message {
	return message{
		Outgoing:   true,
		Timestamp:  time.Now(),
		MediaLabel: "[uploading]",
		Text:       caption,
	}
}

func (mb *mmBackend) Editable(msg message) error {
	if !msg.Outgoing || msg.ID == "" {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	if msg.Multiline {
		return fmt.Errorf("multiline messages cannot be edited in wiregram's single-line editor")
	}
	return nil
}

func (mb *mmBackend) Edit(ctx context.Context, ch chatEntry, msg message, text string) error {
	body := map[string]string{"id": msg.ID, "message": text}
	return mb.apiJSON(ctx, "PUT", "/posts/"+msg.ID+"/patch", body, nil)
}

func (mb *mmBackend) DeleteMessages(ctx context.Context, ch chatEntry, ids []string, forEveryone bool) error {
	for _, id := range ids {
		resp, err := mb.api(ctx, "DELETE", "/posts/"+id, nil)
		if err != nil {
			return fmt.Errorf("delete message: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("delete message: HTTP %d", resp.StatusCode)
		}
	}
	return nil
}

func (mb *mmBackend) DeleteChat(ctx context.Context, ch chatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported on Mattermost")
}

func (mb *mmBackend) MarkRead(ctx context.Context, ch chatEntry, upToID string) error {
	resp, err := mb.api(ctx, "POST", "/channels/members/"+mb.userID+"/view", map[string]any{
		"channel_id": ch.ID.id,
	})
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	resp.Body.Close()
	mb.mu.Lock()
	mb.lastRead[ch.ID.id] = upToID
	mb.mu.Unlock()
	return nil
}

func (mb *mmBackend) SetTyping(ctx context.Context, ch chatEntry) {
	mb.api(ctx, "POST", "/users/"+mb.userID+"/typing", map[string]string{
		"channel_id": ch.ID.id,
	})
}

func (mb *mmBackend) React(ctx context.Context, ch chatEntry, msg message, emoji string) ([]messageReaction, error) {
	var existing []mmReaction
	if err := mb.apiJSON(ctx, "GET", "/posts/"+msg.ID+"/reactions", nil, &existing); err != nil {
		return nil, fmt.Errorf("load reactions: %w", err)
	}
	alreadyReacted := false
	for _, r := range existing {
		if r.UserID == mb.userID && r.EmojiName == emoji {
			alreadyReacted = true
			break
		}
	}
	if alreadyReacted {
		resp, err := mb.api(ctx, "DELETE", fmt.Sprintf("/users/%s/posts/%s/reactions/%s", mb.userID, msg.ID, emoji), nil)
		if err != nil {
			return nil, fmt.Errorf("remove reaction: %w", err)
		}
		resp.Body.Close()
	} else {
		if err := mb.apiJSON(ctx, "POST", "/reactions", mmReaction{
			UserID: mb.userID, PostID: msg.ID, EmojiName: emoji,
		}, nil); err != nil {
			return nil, fmt.Errorf("add reaction: %w", err)
		}
	}
	var updated []mmReaction
	if err := mb.apiJSON(ctx, "GET", "/posts/"+msg.ID+"/reactions", nil, &updated); err != nil {
		return nil, fmt.Errorf("reload reactions: %w", err)
	}
	return mb.aggregateReactions(updated), nil
}

func (mb *mmBackend) Forward(ctx context.Context, from chatEntry, to chatEntry, msgID string) error {
	return fmt.Errorf("forwarding is not supported on Mattermost")
}

func (mb *mmBackend) Pin(ctx context.Context, ch chatEntry, msg message, unpin bool) error {
	action := "pin"
	if unpin {
		action = "unpin"
	}
	resp, err := mb.api(ctx, "POST", "/posts/"+msg.ID+"/"+action, nil)
	if err != nil {
		return fmt.Errorf("%s message: %w", action, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s message: HTTP %d", action, resp.StatusCode)
	}
	return nil
}

func (mb *mmBackend) SearchMessages(ctx context.Context, ch chatEntry, query string) ([]message, error) {
	body := map[string]any{
		"terms":        query,
		"is_or_search": false,
	}
	raw, ok := ch.Raw.(mmChannel)
	if ok {
		body["terms"] = "in:" + raw.Name + " " + query
	}
	var result struct {
		Order []string          `json:"order"`
		Posts map[string]mmPost `json:"posts"`
	}
	if err := mb.apiJSON(ctx, "POST", fmt.Sprintf("/teams/%s/posts/search", mb.teamForChannel(ctx, ch)), body, &result); err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	messages := make([]message, 0, len(result.Order))
	for _, id := range result.Order {
		if p, ok := result.Posts[id]; ok && p.DeleteAt == 0 {
			messages = append(messages, mb.postToMessage(ctx, p))
		}
	}
	return messages, nil
}

func (mb *mmBackend) teamForChannel(ctx context.Context, ch chatEntry) string {
	if raw, ok := ch.Raw.(mmChannel); ok && raw.TeamID != "" {
		return raw.TeamID
	}
	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil || len(teams) == 0 {
		return ""
	}
	return teams[0].ID
}

func (mb *mmBackend) Subscribe(ch chatEntry) {
	mb.mu.Lock()
	mb.subscribed = ch.ID.id
	mb.pending = nil
	mb.mu.Unlock()
}

func (mb *mmBackend) Unsubscribe() {
	mb.mu.Lock()
	mb.subscribed = ""
	mb.pending = nil
	mb.mu.Unlock()
}

func (mb *mmBackend) Drain() ([]message, []genericReactionUpdate, []string) {
	mb.mu.Lock()
	events := mb.pending
	mb.pending = nil
	mb.mu.Unlock()

	var messages []message
	var reactions []genericReactionUpdate
	var deletions []string
	ctx := context.Background()

	for _, ev := range events {
		switch ev.kind {
		case "post":
			if ev.post != nil {
				messages = append(messages, mb.postToMessage(ctx, *ev.post))
			}
		case "edit":
			if ev.post != nil {
				messages = append(messages, mb.postToMessage(ctx, *ev.post))
			}
		case "delete":
			if ev.post != nil {
				deletions = append(deletions, ev.post.ID)
			}
		case "reaction":
			if ev.post != nil {
				var allReactions []mmReaction
				mb.apiJSON(ctx, "GET", "/posts/"+ev.post.ID+"/reactions", nil, &allReactions)
				reactions = append(reactions, genericReactionUpdate{
					MessageID: ev.post.ID,
					Reactions: mb.aggregateReactions(allReactions),
				})
			}
		}
	}
	return messages, reactions, deletions
}

func (mb *mmBackend) Changed() <-chan struct{} { return mb.changed }

func (mb *mmBackend) IsTyping() bool {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	for uid, t := range mb.typing {
		if uid != mb.userID && time.Since(t) < 6*time.Second {
			return true
		}
	}
	return false
}

func (mb *mmBackend) ClearTyping() {
	mb.mu.Lock()
	mb.typing = make(map[string]time.Time)
	mb.mu.Unlock()
}

func (mb *mmBackend) ReadMarker(ch chatEntry, msg message) string {
	return ""
}

func (mb *mmBackend) RefreshReadState(ctx context.Context, ch chatEntry) error {
	return nil
}

func (mb *mmBackend) RefreshChatUnread(ctx context.Context, ch chatEntry) (int, error) {
	mb.mu.Lock()
	n := mb.unreads[ch.ID.id]
	mb.mu.Unlock()
	return n, nil
}

func (mb *mmBackend) CachedUnread(ch chatEntry) int {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return mb.unreads[ch.ID.id]
}

func (mb *mmBackend) InboxPosition(ch chatEntry) string {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return mb.lastRead[ch.ID.id]
}

func (mb *mmBackend) MediaPlayable(msg message) bool { return false }

func (mb *mmBackend) PlayMedia(ctx context.Context, t *terminal, msg message, player string) error {
	return fmt.Errorf("media playback is not supported on Mattermost")
}

func (mb *mmBackend) MediaSaveable(msg message) (string, bool) {
	p, ok := msg.Raw.(mmPost)
	if !ok {
		return "", false
	}
	if p.Metadata != nil && len(p.Metadata.Files) > 0 {
		return p.Metadata.Files[0].Name, true
	}
	if len(p.FileIDs) > 0 {
		return "attachment", true
	}
	return "", false
}

func (mb *mmBackend) SaveMedia(ctx context.Context, msg message, destDir string) error {
	p, ok := msg.Raw.(mmPost)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	var fileID, filename string
	if p.Metadata != nil && len(p.Metadata.Files) > 0 {
		fileID = p.Metadata.Files[0].ID
		filename = p.Metadata.Files[0].Name
	} else if len(p.FileIDs) > 0 {
		fileID = p.FileIDs[0]
		filename = "attachment"
	} else {
		return fmt.Errorf("this message has no downloadable attachment")
	}

	resp, err := mb.api(ctx, "GET", "/files/"+fileID, nil)
	if err != nil {
		return fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download file: HTTP %d", resp.StatusCode)
	}
	dest := filepath.Join(destDir, filename)
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err := firstError(copyErr, closeErr); err != nil {
		return fmt.Errorf("save file: %w", err)
	}
	return nil
}

func firstError(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func (mb *mmBackend) AcknowledgeMedia(ctx context.Context, msg message) {}

func (mb *mmBackend) SearchUsers(ctx context.Context, t *terminal, recentChats []chatEntry) (chatEntry, bool, error) {
	type mmUserChoice struct {
		user  mmUser
		label string
		match string
	}

	toChoice := func(u mmUser) mmUserChoice {
		label := u.displayName()
		if u.Username != "" && !strings.EqualFold(label, u.Username) {
			label += "  @" + u.Username
		}
		match := strings.ToLower(u.Username + " " + u.First + " " + u.Last + " " + u.Nickname)
		return mmUserChoice{user: u, label: label, match: match}
	}

	var base []mmUserChoice
	for _, ch := range recentChats {
		raw, ok := ch.Raw.(mmChannel)
		if !ok || raw.Type != "D" {
			continue
		}
		parts := strings.Split(raw.Name, "__")
		for _, uid := range parts {
			if uid != mb.userID && uid != "" {
				u := mb.resolveUser(ctx, uid)
				base = append(base, toChoice(u))
			}
		}
	}

	choices := base
	heading := "Recent DMs"
	selected := 0
	var query []rune
	searching := false
	notice := ""
	generation := 0
	type searchResult struct {
		gen     int
		choices []mmUserChoice
		err     error
	}
	results := make(chan searchResult, 4)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var timerC <-chan time.Time
	var searchCancel context.CancelFunc
	defer func() {
		if searchCancel != nil {
			searchCancel()
		}
	}()

	render := func() {
		labels := make([]userChoice, len(choices))
		for i, c := range choices {
			labels[i] = userChoice{label: c.label}
		}
		renderUserSearch(t.out(), labels, selected, string(query), heading, notice, searching)
	}

	filter := func(all []mmUserChoice, q string) []mmUserChoice {
		q = strings.ToLower(strings.TrimSpace(q))
		if q == "" {
			return nil
		}
		var out []mmUserChoice
		for _, c := range all {
			if strings.Contains(c.match, q) {
				out = append(out, c)
			}
		}
		return out
	}

	resetSearch := func() {
		generation++
		if searchCancel != nil {
			searchCancel()
			searchCancel = nil
		}
		if !timer.Stop() && timerC != nil {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
		text := strings.TrimSpace(string(query))
		selected = 0
		searching = false
		notice = ""
		if text == "" {
			choices, heading = base, "Recent DMs"
			return
		}
		choices = filter(base, text)
		heading = "Results"
		if len(text) >= 2 {
			searching = true
			timer.Reset(300 * time.Millisecond)
			timerC = timer.C
		}
	}

	render()
	for {
		select {
		case <-ctx.Done():
			return chatEntry{}, false, ctx.Err()
		case <-timerC:
			timerC = nil
			searchCtx, cancel := context.WithCancel(ctx)
			searchCancel = cancel
			gen, text := generation, strings.TrimSpace(string(query))
			go func() {
				var users []mmUser
				err := mb.apiJSON(searchCtx, "POST", "/users/search", map[string]string{"term": text}, &users)
				var out []mmUserChoice
				if err == nil {
					seen := make(map[string]bool)
					for _, u := range users {
						if u.ID == mb.userID || seen[u.ID] {
							continue
						}
						seen[u.ID] = true
						out = append(out, toChoice(u))
					}
				}
				select {
				case results <- searchResult{gen, out, err}:
				case <-searchCtx.Done():
				}
			}()
		case result := <-results:
			if result.gen != generation {
				continue
			}
			searching = false
			if searchCancel != nil {
				searchCancel()
			}
			searchCancel = nil
			if result.err != nil {
				t.report(result.err)
				notice = cleanError(result.err)
				render()
				continue
			}
			notice = ""
			seen := make(map[string]bool)
			var merged []mmUserChoice
			text := strings.TrimSpace(string(query))
			for _, c := range filter(base, text) {
				if !seen[c.user.ID] {
					seen[c.user.ID] = true
					merged = append(merged, c)
				}
			}
			for _, c := range result.choices {
				if !seen[c.user.ID] {
					seen[c.user.ID] = true
					merged = append(merged, c)
				}
			}
			choices = merged
			selected = min(selected, max(0, len(choices)-1))
			render()
		case key, ok := <-t.keys:
			if !ok {
				return chatEntry{}, false, context.Canceled
			}
			if key == terminalLockKey {
				continue
			}
			if key == terminalUnlockKey {
				render()
				t.flushNotices()
				continue
			}
			switch key {
			case "\x03", "\x04":
				return chatEntry{}, false, nil
			case "\x1b":
				if len(query) == 0 {
					return chatEntry{}, false, nil
				}
				query = nil
				resetSearch()
			case "\x1b[A", "\x1bOA":
				if selected > 0 {
					selected--
				}
			case "\x1b[B", "\x1bOB":
				if selected+1 < len(choices) {
					selected++
				}
			case "\r", "\n":
				if selected < len(choices) {
					user := choices[selected].user
					notice = "Opening…"
					render()
					entry, err := mb.openDM(ctx, user)
					if err != nil {
						t.report(err)
						notice = cleanError(err)
						render()
						continue
					}
					return entry, true, nil
				}
			case "\x7f", "\x08":
				if len(query) > 0 {
					query = query[:len(query)-1]
					resetSearch()
				}
			case "\x15":
				if len(query) > 0 {
					query = nil
					resetSearch()
				}
			default:
				if strings.HasPrefix(key, "\x1b") {
					render()
					continue
				}
				previous := len(query)
				query = appendQuery(query, key)
				if len(query) != previous {
					resetSearch()
				}
			}
			render()
		}
	}
}

func (mb *mmBackend) openDM(ctx context.Context, user mmUser) (chatEntry, error) {
	var ch mmChannel
	if err := mb.apiJSON(ctx, "POST", "/channels/direct", []string{mb.userID, user.ID}, &ch); err != nil {
		return chatEntry{}, fmt.Errorf("open DM with %s: %w", user.displayName(), err)
	}
	mb.mu.Lock()
	mb.userCache[user.ID] = user
	mb.mu.Unlock()
	return chatEntry{
		ID:    chatID{backend: "mattermost", id: ch.ID},
		Title: user.displayName(),
		IsDM:  true,
		Raw:   ch,
	}, nil
}

func (mb *mmBackend) HandleForward(ctx context.Context, t *terminal, recentChats []chatEntry, fromChat chatEntry, msgID string) {
}

func (mb *mmBackend) AIAvailable() bool                   { return false }
func (mb *mmBackend) AIOn(ch chatEntry) bool              { return false }
func (mb *mmBackend) AIToggle(ch chatEntry) (bool, error) { return false, nil }
func (mb *mmBackend) AIShouldAnswer(msg message) bool     { return false }
func (mb *mmBackend) AIRequest(ch chatEntry, msgs []message) (aiPrompt, error) {
	return aiPrompt{}, nil
}
func (mb *mmBackend) AIComplete(ctx context.Context, prompt aiPrompt) (string, error) {
	return "", fmt.Errorf("AI is not available for Mattermost")
}

func (mb *mmBackend) RecentEmoji() []string { return mb.recentEmoji }

func (mb *mmBackend) RememberEmoji(value string) {
	for i, v := range mb.recentEmoji {
		if v == value {
			mb.recentEmoji = append(mb.recentEmoji[:i], mb.recentEmoji[i+1:]...)
			break
		}
	}
	mb.recentEmoji = append([]string{value}, mb.recentEmoji...)
	if len(mb.recentEmoji) > 20 {
		mb.recentEmoji = mb.recentEmoji[:20]
	}
}

func (mb *mmBackend) EmojiChoices(query string) []emojiOption {
	catalog := emojiCatalog()
	if query == "" {
		recent := mb.recentEmoji
		if len(recent) == 0 {
			if len(catalog) > 10 {
				catalog = catalog[:10]
			}
			return catalog
		}
		var choices []emojiOption
		for _, v := range recent {
			choices = append(choices, emojiOption{value: v, name: v})
		}
		return choices
	}
	q := strings.ToLower(query)
	var matches []emojiOption
	for _, e := range catalog {
		if strings.Contains(strings.ToLower(e.name), q) || strings.Contains(e.value, q) {
			matches = append(matches, e)
			if len(matches) >= 10 {
				break
			}
		}
	}
	return matches
}

func (mb *mmBackend) ReactionChoices(query string) []emojiOption {
	return mb.EmojiChoices(query)
}

func (mb *mmBackend) notify() {
	select {
	case mb.changed <- struct{}{}:
	default:
	}
}

func (mb *mmBackend) startWebSocket(ctx context.Context) {
	wsCtx, cancel := context.WithCancel(ctx)
	mb.wsStop = cancel

	go func() {
		delay := time.Second
		for wsCtx.Err() == nil {
			if err := mb.runWebSocket(wsCtx); err != nil {
				if wsCtx.Err() != nil {
					return
				}
			}
			select {
			case <-wsCtx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, 30*time.Second)
		}
	}()
}

func (mb *mmBackend) runWebSocket(ctx context.Context) error {
	wsURL := strings.Replace(mb.serverURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/api/v4/websocket"

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	mb.wsConn = conn

	auth := map[string]any{
		"seq":    1,
		"action": "authentication_challenge",
		"data":   map[string]string{"token": mb.token},
	}
	authData, _ := json.Marshal(auth)
	if err := conn.Write(ctx, websocket.MessageText, authData); err != nil {
		return fmt.Errorf("websocket auth: %w", err)
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("websocket read: %w", err)
		}
		mb.handleWSEvent(data)
	}
}

func (mb *mmBackend) handleWSEvent(data []byte) {
	var event struct {
		Event     string          `json:"event"`
		Data      json.RawMessage `json:"data"`
		Broadcast struct {
			ChannelID string `json:"channel_id"`
			UserID    string `json:"user_id"`
		} `json:"broadcast"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	mb.mu.Lock()
	channelID := event.Broadcast.ChannelID
	subscribed := mb.subscribed
	mb.mu.Unlock()

	switch event.Event {
	case "posted":
		var wrapper struct {
			Post string `json:"post"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			return
		}
		var post mmPost
		if err := json.Unmarshal([]byte(wrapper.Post), &post); err != nil {
			return
		}
		if post.ChannelID == subscribed {
			mb.mu.Lock()
			mb.pending = append(mb.pending, mmEvent{kind: "post", post: &post})
			mb.mu.Unlock()
			mb.notify()
		}

	case "post_edited":
		var wrapper struct {
			Post string `json:"post"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			return
		}
		var post mmPost
		if err := json.Unmarshal([]byte(wrapper.Post), &post); err != nil {
			return
		}
		if post.ChannelID == subscribed {
			mb.mu.Lock()
			mb.pending = append(mb.pending, mmEvent{kind: "edit", post: &post})
			mb.mu.Unlock()
			mb.notify()
		}

	case "post_deleted":
		var wrapper struct {
			Post string `json:"post"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			return
		}
		var post mmPost
		if err := json.Unmarshal([]byte(wrapper.Post), &post); err != nil {
			return
		}
		if post.ChannelID == subscribed {
			mb.mu.Lock()
			mb.pending = append(mb.pending, mmEvent{kind: "delete", post: &post})
			mb.mu.Unlock()
			mb.notify()
		}

	case "reaction_added", "reaction_removed":
		var wrapper struct {
			Reaction string `json:"reaction"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			return
		}
		var reaction mmReaction
		if err := json.Unmarshal([]byte(wrapper.Reaction), &reaction); err != nil {
			return
		}
		if channelID == subscribed {
			mb.mu.Lock()
			mb.pending = append(mb.pending, mmEvent{kind: "reaction", post: &mmPost{ID: reaction.PostID}})
			mb.mu.Unlock()
			mb.notify()
		}

	case "typing":
		if channelID == subscribed && event.Broadcast.UserID != mb.userID {
			mb.mu.Lock()
			mb.typing[event.Broadcast.UserID] = time.Now()
			mb.mu.Unlock()
			mb.notify()
		}
	}
}
