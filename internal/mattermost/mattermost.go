package mattermost

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"wiregram/internal/backend"
	"wiregram/internal/config"
	"wiregram/internal/textfmt"
)

type MmBackend struct {
	ServerURL string
	Token     string
	UserID    string
	Username  string

	mu          sync.Mutex
	subscribed  string               // channel ID
	typing      map[string]time.Time // userID → last typing event
	unreads     map[string]int       // channelID → unread count
	lastRead    map[string]string    // channelID → last read post ID
	userCache   map[string]mmUser
	recentEmoji []string

	events backend.Bus

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
	Props     map[string]any  `json:"props,omitempty"`
}

type mmPostMetadata struct {
	Files  []mmFileInfo `json:"files,omitempty"`
	Embeds []mmEmbed    `json:"embeds,omitempty"`
}

type mmEmbed struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Data struct {
		SiteName    string `json:"site_name"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"data"`
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

type mmReaction struct {
	UserID    string `json:"user_id"`
	PostID    string `json:"post_id"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at"`
}

func NewMMBackend(serverURL string) *MmBackend {
	return &MmBackend{
		ServerURL: strings.TrimRight(serverURL, "/"),
		typing:    make(map[string]time.Time),
		unreads:   make(map[string]int),
		lastRead:  make(map[string]string),
		userCache: make(map[string]mmUser),
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (mb *MmBackend) Name() string { return "mattermost" }

func (mb *MmBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		Forward:       false,
		DeleteForMe:   false,
		OutboxReceipt: false,
		ViewOnce:      false,
		Reactions:     true,
		Edit:          true,
		Pin:           true,
		Upload:        true,
		Search:        true,
		ChannelSearch: true,
		Typing:        true,
		DeleteChat:    false,
	}
}

func (mb *MmBackend) api(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, mb.ServerURL+"/api/v4"+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mb.Token != "" {
		req.Header.Set("Authorization", "Bearer "+mb.Token)
	}
	return mb.client.Do(req)
}

func (mb *MmBackend) apiJSON(ctx context.Context, method, path string, body, result any) error {
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

func (mb *MmBackend) Run(ctx context.Context) error {
	mb.StartWebSocket(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (mb *MmBackend) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	if mb.Token != "" {
		var me mmUser
		if err := mb.apiJSON(ctx, "GET", "/users/me", nil, &me); err == nil {
			mb.UserID = me.ID
			mb.Username = me.Username
			mb.mu.Lock()
			mb.userCache[me.ID] = me
			mb.mu.Unlock()
			return nil
		}
		mb.Token = ""
	}

	if prompt == nil {
		return fmt.Errorf("Mattermost account needs authentication")
	}

	email, err := prompt("Email or username: ", false)
	if err != nil {
		return fmt.Errorf("read email: %w", err)
	}
	password, err := prompt("Password: ", true)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	email, password = strings.TrimSpace(email), strings.TrimSpace(password)
	resp, err := mb.login(ctx, email, password, "")
	var apiErr *mmAPIError
	if errors.As(err, &apiErr) && apiErr.mfaRequired() {
		code, perr := prompt("Authenticator code: ", false)
		if perr != nil {
			return fmt.Errorf("read authenticator code: %w", perr)
		}
		resp, err = mb.login(ctx, email, password, strings.TrimSpace(code))
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	mb.Token = resp.Header.Get("Token")
	var me mmUser
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	mb.UserID = me.ID
	mb.Username = me.Username
	mb.mu.Lock()
	mb.userCache[me.ID] = me
	mb.mu.Unlock()
	return nil
}

type mmAPIError struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func (e *mmAPIError) Error() string { return "login failed: " + e.Message }

// mfaRequired reports a login refused for lacking an MFA code; depending on
// the server version an empty code fails to parse or fails to validate.
func (e *mmAPIError) mfaRequired() bool {
	return e.ID == "mfa.validate_token.authenticate.app_error" ||
		e.ID == "api.user.check_user_mfa.bad_code.app_error"
}

// login posts the credentials, with the MFA code when given, and returns the
// successful response for the caller to read and close.
func (mb *MmBackend) login(ctx context.Context, loginID, password, code string) (*http.Response, error) {
	body := map[string]string{"login_id": loginID, "password": password}
	if code != "" {
		body["token"] = code
	}
	resp, err := mb.api(ctx, "POST", "/users/login", body)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var apiErr mmAPIError
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Message != "" {
		return nil, &apiErr
	}
	return nil, fmt.Errorf("login failed: %s", strings.TrimSpace(string(data)))
}

func (mb *MmBackend) Identity() backend.AccountIdentity {
	return backend.AccountIdentity{Name: mb.Username}
}

func (mb *MmBackend) AccountLabel() string {
	host := mb.ServerURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	return fmt.Sprintf("[MM] %s@%s", mb.Username, host)
}

func (mb *MmBackend) Logout(ctx context.Context) error {
	if err := mb.apiJSON(ctx, "POST", "/users/logout", nil, nil); err != nil {
		return fmt.Errorf("log out of Mattermost: %w", err)
	}
	return nil
}

func (mb *MmBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}

	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil {
		return nil, 0, fmt.Errorf("load teams: %w", err)
	}

	type channelWithUnread struct {
		Ch       mmChannel
		Unread   int
		TeamName string
	}
	teamNames := make(map[string]string, len(teams))
	for _, team := range teams {
		teamNames[team.ID] = team.DisplayName
	}

	// Fetch channels and member data concurrently.
	type memberResult struct {
		Members []mmChannelMember
		Err     error
	}
	memberCh := make(chan memberResult, len(teams))
	for _, team := range teams {
		go func(teamID string) {
			var members []mmChannelMember
			err := mb.apiJSON(ctx, "GET", fmt.Sprintf("/users/me/teams/%s/channels/members", teamID), nil, &members)
			memberCh <- memberResult{members, err}
		}(team.ID)
	}

	// Fetch first two pages of channels (most recent activity first).
	var allChannels []mmChannel
	for page := 0; page < 2; page++ {
		var batch []mmChannel
		if err := mb.apiJSON(ctx, "GET", fmt.Sprintf("/users/me/channels?page=%d&per_page=100", page), nil, &batch); err != nil {
			return nil, 0, fmt.Errorf("load channels: %w", err)
		}
		allChannels = append(allChannels, batch...)
		if len(batch) < 100 {
			break
		}
	}

	// Collect member results.
	memberMap := make(map[string]mmChannelMember)
	for range teams {
		res := <-memberCh
		if res.Err != nil {
			continue
		}
		for _, m := range res.Members {
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
		return channels[i].Ch.LastPostAt > channels[j].Ch.LastPostAt
	})

	if len(channels) > 20 {
		channels = channels[:20]
	}

	chansOnly := make([]mmChannel, len(channels))
	for i, cw := range channels {
		chansOnly[i] = cw.Ch
	}
	mb.batchResolveChannelUsers(ctx, chansOnly)
	entries := make([]backend.ChatEntry, 0, len(channels))
	for _, cw := range channels {
		title := mb.channelTitle(ctx, cw.Ch, cw.TeamName)
		isDM := cw.Ch.Type == "D" || cw.Ch.Type == "G"
		mb.mu.Lock()
		mb.unreads[cw.Ch.ID] = cw.Unread
		mb.mu.Unlock()
		entries = append(entries, backend.ChatEntry{
			ID:      backend.ChatID{Backend: "mattermost", Id: cw.Ch.ID},
			Title:   title,
			Unread:  cw.Unread,
			IsDM:    isDM,
			IsGroup: !isDM,
			Raw:     cw.Ch,
		})
	}
	return entries, 0, nil
}

func (mb *MmBackend) channelTitle(ctx context.Context, ch mmChannel, teamName string) string {
	switch ch.Type {
	case "D":
		parts := strings.Split(ch.Name, "__")
		for _, uid := range parts {
			if uid != mb.UserID && uid != "" {
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

func (mb *MmBackend) batchResolveChannelUsers(ctx context.Context, channels []mmChannel) {
	var needed []string
	mb.mu.Lock()
	for _, ch := range channels {
		if ch.Type != "D" {
			continue
		}
		for _, uid := range strings.Split(ch.Name, "__") {
			if uid != "" && uid != mb.UserID {
				if _, ok := mb.userCache[uid]; !ok {
					needed = append(needed, uid)
				}
			}
		}
	}
	mb.mu.Unlock()
	if len(needed) == 0 {
		return
	}
	var users []mmUser
	if err := mb.apiJSON(ctx, "POST", "/users/ids", needed, &users); err != nil {
		return
	}
	mb.mu.Lock()
	for _, u := range users {
		mb.userCache[u.ID] = u
	}
	mb.mu.Unlock()
}

func (mb *MmBackend) resolveUser(ctx context.Context, userID string) mmUser {
	mb.mu.Lock()
	if u, ok := mb.userCache[userID]; ok {
		mb.mu.Unlock()
		return u
	}
	mb.mu.Unlock()

	if userID == "" {
		return mmUser{}
	}
	short := userID
	if len(short) > 8 {
		short = short[:8]
	}
	var u mmUser
	if err := mb.apiJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, &u); err != nil {
		return mmUser{ID: userID, Username: short}
	}
	mb.mu.Lock()
	mb.userCache[userID] = u
	mb.mu.Unlock()
	return u
}

func (mb *MmBackend) postToMessage(ctx context.Context, p mmPost) backend.Message {
	sender := ""
	senderID := p.UserID
	outgoing := p.UserID == mb.UserID
	if !outgoing {
		u := mb.resolveUser(ctx, p.UserID)
		sender = u.displayName()
	}
	// Webhooks post as the user who created them and may name themselves.
	if name, ok := p.Props["override_username"].(string); ok && name != "" &&
		p.Props["from_webhook"] == "true" {
		sender = name
	}
	text := p.Message
	if att := mmAttachmentsText(p.Props); att != "" {
		if text != "" {
			text += "\n"
		}
		text += att
	}
	text, spans := backend.Markdown(text)

	var mediaLabel string
	image := false
	if p.Metadata != nil {
		// SaveMedia fetches the first file, so only that one can be drawn.
		if files := p.Metadata.Files; len(files) > 0 {
			image = strings.HasPrefix(files[0].MimeType, "image/") &&
				files[0].Size <= backend.MaxInlineImage
		}
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

	return backend.Message{
		ID:         p.ID,
		Sender:     sender,
		SenderID:   senderID,
		Text:       text,
		Timestamp:  time.UnixMilli(p.CreateAt).Local(),
		Outgoing:   outgoing,
		Pinned:     p.IsPinned,
		EditDate:   editDate,
		ReplyToID:  p.RootID,
		MediaLabel: mediaLabel,
		Image:      image,
		Multiline:  strings.Contains(text, "\n"),
		Preview:    mmPreview(p.Metadata),
		Spans:      spans,
		Source:     p.Message,
		Mentioned:  !outgoing && mentionsName(p.Message, mb.Username),
		Raw:        p,
	}
}

// mentionsName reports whether text holds @name or a channel-wide mention,
// matched as Mattermost does: case-insensitive, ending at a non-name rune.
func mentionsName(text, name string) bool {
	lower := strings.ToLower(text)
	for _, n := range []string{strings.ToLower(name), "channel", "all", "here"} {
		if n == "" {
			continue
		}
		for rest := lower; ; {
			i := strings.Index(rest, "@"+n)
			if i < 0 {
				break
			}
			end := i + 1 + len(n)
			if i > 0 && mmNameByte(rest[i-1]) {
				rest = rest[end:]
				continue
			}
			if end == len(rest) || !mmNameByte(rest[end]) || rest[end] == '.' &&
				(end+1 == len(rest) || !mmNameByte(rest[end+1])) {
				return true
			}
			rest = rest[end:]
		}
	}
	return false
}

// mmNameByte reports whether c can continue a Mattermost username.
func mmNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// mmAttachment is a Slack-style message attachment, which bots and webhooks
// such as alert relays post in props instead of message text.
type mmAttachment struct {
	Fallback   string `json:"fallback"`
	Pretext    string `json:"pretext"`
	AuthorName string `json:"author_name"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Fields     []struct {
		Title string `json:"title"`
		Value any    `json:"value"`
	} `json:"fields"`
	Footer string `json:"footer"`
}

// mmAttachmentsText flattens a post's attachments into plain lines.
func mmAttachmentsText(props map[string]any) string {
	raw, ok := props["attachments"]
	if !ok {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var atts []mmAttachment
	if json.Unmarshal(data, &atts) != nil {
		return ""
	}
	var lines []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			lines = append(lines, s)
		}
	}
	for _, a := range atts {
		before := len(lines)
		add(a.Pretext)
		add(a.AuthorName)
		if t := strings.TrimSpace(a.Title); t != "" && !strings.Contains(t, "*") {
			add("**" + t + "**")
		} else {
			add(t)
		}
		add(a.Text)
		for _, f := range a.Fields {
			v := strings.TrimSpace(fmt.Sprint(f.Value))
			if f.Value == nil || v == "" {
				continue
			}
			if f.Title != "" {
				v = "**" + f.Title + ":** " + v
			}
			add(v)
		}
		add(a.Footer)
		if len(lines) == before {
			add(a.Fallback)
		}
	}
	return strings.Join(lines, "\n")
}

// mmPreview returns the first OpenGraph embed the server attached.
func mmPreview(md *mmPostMetadata) *backend.LinkPreview {
	if md == nil {
		return nil
	}
	for _, e := range md.Embeds {
		if e.Type != "opengraph" || (e.Data.Title == "" && e.Data.Description == "") {
			continue
		}
		site := e.Data.SiteName
		if site == "" {
			if u, err := url.Parse(e.URL); err == nil {
				site = u.Hostname()
			}
		}
		return &backend.LinkPreview{
			URL: e.URL, Site: site, Title: e.Data.Title, Description: e.Data.Description,
		}
	}
	return nil
}

func mmFileLabel(f mmFileInfo) string {
	kind := "file"
	if strings.HasPrefix(f.MimeType, "image/") {
		kind = "photo"
		if f.Width > 0 {
			return fmt.Sprintf("[%s] %dx%d, %s", kind, f.Width, f.Height, textfmt.FormatBytes(f.Size))
		}
	} else if strings.HasPrefix(f.MimeType, "video/") {
		kind = "video"
	} else if strings.HasPrefix(f.MimeType, "audio/") {
		kind = "audio"
	}
	return fmt.Sprintf("[%s] %s, %s", kind, f.Name, textfmt.FormatBytes(f.Size))
}

func (mb *MmBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	return mb.fetchPosts(ctx, ch.ID.Id, "", 25)
}

func (mb *MmBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	return mb.fetchPosts(ctx, ch.ID.Id, beforeID, 25)
}

// UnreadMessages reads the newest posts, which the unread count covers, and
// skips the per-post reaction requests a History page makes.
func (mb *MmBackend) UnreadMessages(ctx context.Context, ch backend.ChatEntry, limit int) ([]backend.Message, error) {
	msgs, err := mb.loadPosts(ctx, ch.ID.Id, "", limit)
	if err != nil {
		return nil, err
	}
	return backend.NewestIncoming(msgs, limit), nil
}

func (mb *MmBackend) Messages(ctx context.Context, ch backend.ChatEntry, ids []string) ([]backend.Message, error) {
	var msgs []backend.Message
	for _, id := range ids {
		var p mmPost
		if err := mb.apiJSON(ctx, "GET", "/posts/"+url.PathEscape(id), nil, &p); err != nil {
			// A deleted root must not hide the others.
			continue
		}
		if p.DeleteAt == 0 {
			msgs = append(msgs, mb.postToMessage(ctx, p))
		}
	}
	return msgs, nil
}

func (mb *MmBackend) Thread(ctx context.Context, ch backend.ChatEntry, rootID string) ([]backend.Message, error) {
	var result struct {
		Posts map[string]mmPost `json:"posts"`
	}
	if err := mb.apiJSON(ctx, "GET", "/posts/"+url.PathEscape(rootID)+"/thread", nil,
		&result); err != nil {
		return nil, fmt.Errorf("load thread: %w", err)
	}
	posts := make([]mmPost, 0, len(result.Posts))
	for _, p := range result.Posts {
		if p.DeleteAt == 0 {
			posts = append(posts, p)
		}
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].CreateAt < posts[j].CreateAt })
	messages := make([]backend.Message, len(posts))
	for i, p := range posts {
		messages[i] = mb.postToMessage(ctx, p)
	}
	mb.loadReactions(ctx, messages)
	return messages, nil
}

func (mb *MmBackend) fetchPosts(ctx context.Context, channelID, before string, perPage int) ([]backend.Message, error) {
	messages, err := mb.loadPosts(ctx, channelID, before, perPage)
	if err != nil {
		return nil, err
	}
	mb.loadReactions(ctx, messages)
	return messages, nil
}

func (mb *MmBackend) loadPosts(ctx context.Context, channelID, before string, perPage int) ([]backend.Message, error) {
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
	messages := make([]backend.Message, 0, len(result.Order))
	for i := len(result.Order) - 1; i >= 0; i-- {
		p, ok := result.Posts[result.Order[i]]
		if !ok || p.DeleteAt > 0 {
			continue
		}
		messages = append(messages, mb.postToMessage(ctx, p))
	}
	return messages, nil
}

func (mb *MmBackend) loadReactions(ctx context.Context, messages []backend.Message) {
	for i, msg := range messages {
		var reactions []mmReaction
		if err := mb.apiJSON(ctx, "GET", "/posts/"+msg.ID+"/reactions", nil, &reactions); err != nil {
			continue
		}
		messages[i].Reactions = mb.aggregateReactions(reactions)
	}
}

func (mb *MmBackend) aggregateReactions(reactions []mmReaction) []backend.MessageReaction {
	counts := make(map[string]*backend.MessageReaction)
	var order []string
	for _, r := range reactions {
		if _, ok := counts[r.EmojiName]; !ok {
			order = append(order, r.EmojiName)
			counts[r.EmojiName] = &backend.MessageReaction{Emoji: reactionLabel(r.EmojiName)}
		}
		mr := counts[r.EmojiName]
		mr.Count++
		if r.UserID == mb.UserID {
			mr.Chosen = true
		}
	}
	result := make([]backend.MessageReaction, 0, len(order))
	for _, name := range order {
		result = append(result, *counts[name])
	}
	return result
}

// reactionLabel shows a reaction as its emoji. Custom emoji are server-side
// images, so they keep their :name:.
func reactionLabel(name string) string {
	if emoji, ok := systemEmoji(name); ok {
		return emoji
	}
	return ":" + name + ":"
}

// reactionName turns a picked emoji into the short name Mattermost stores.
func reactionName(emoji string) string {
	if name, ok := systemEmojiName(strings.ReplaceAll(emoji, "\ufe0f", "")); ok {
		return name
	}
	return strings.Trim(emoji, ":")
}

func (mb *MmBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	post := map[string]string{
		"channel_id": ch.ID.Id,
		"message":    text,
	}
	if replyTo != "" {
		post["root_id"] = replyTo
	}
	var result mmPost
	if err := mb.apiJSON(ctx, "POST", "/posts", post, &result); err != nil {
		return backend.Message{}, fmt.Errorf("send message: %w", err)
	}
	return mb.postToMessage(ctx, result), nil
}

func (mb *MmBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, path, caption, replyTo string) (backend.Message, error) {
	fileIDs, err := mb.uploadFile(ctx, ch.ID.Id, path)
	if err != nil {
		return backend.Message{}, err
	}
	post := struct {
		ChannelID string   `json:"channel_id"`
		Message   string   `json:"message"`
		RootID    string   `json:"root_id,omitempty"`
		FileIDs   []string `json:"file_ids"`
	}{
		ChannelID: ch.ID.Id,
		Message:   caption,
		RootID:    replyTo,
		FileIDs:   fileIDs,
	}
	var result mmPost
	if err := mb.apiJSON(ctx, "POST", "/posts", post, &result); err != nil {
		return backend.Message{}, fmt.Errorf("send file message: %w", err)
	}
	return mb.postToMessage(ctx, result), nil
}

func (mb *MmBackend) uploadFile(ctx context.Context, channelID, filePath string) ([]string, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST", mb.ServerURL+"/api/v4/files", &buf)
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+mb.Token)
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

func (mb *MmBackend) UploadPlaceholder(path, caption string) backend.Message {
	return backend.Message{
		Outgoing:   true,
		Timestamp:  time.Now(),
		MediaLabel: "[uploading]",
		Text:       caption,
	}
}

func (mb *MmBackend) Editable(msg backend.Message) error {
	if !msg.Outgoing || msg.ID == "" {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	if msg.Multiline {
		return fmt.Errorf("multiline messages cannot be edited in wiregram's single-line editor")
	}
	return nil
}

func (mb *MmBackend) Edit(ctx context.Context, ch backend.ChatEntry, msg backend.Message, text string) error {
	body := map[string]string{"id": msg.ID, "message": text}
	return mb.apiJSON(ctx, "PUT", "/posts/"+msg.ID+"/patch", body, nil)
}

func (mb *MmBackend) DeleteMessages(ctx context.Context, ch backend.ChatEntry, ids []string, forEveryone bool) error {
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

func (mb *MmBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported on Mattermost")
}

func (mb *MmBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	resp, err := mb.api(ctx, "POST", "/channels/members/"+mb.UserID+"/view", map[string]any{
		"channel_id": ch.ID.Id,
	})
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	resp.Body.Close()
	mb.mu.Lock()
	mb.lastRead[ch.ID.Id] = upToID
	mb.unreads[ch.ID.Id] = 0
	mb.mu.Unlock()
	return nil
}

func (mb *MmBackend) SetTyping(ctx context.Context, ch backend.ChatEntry) {
	resp, err := mb.api(ctx, "POST", "/users/"+mb.UserID+"/typing", map[string]string{
		"channel_id": ch.ID.Id,
	})
	if err == nil {
		resp.Body.Close()
	}
}

func (mb *MmBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
	emoji = reactionName(emoji)
	var existing []mmReaction
	if err := mb.apiJSON(ctx, "GET", "/posts/"+msg.ID+"/reactions", nil, &existing); err != nil {
		return nil, fmt.Errorf("load reactions: %w", err)
	}
	alreadyReacted := false
	for _, r := range existing {
		if r.UserID == mb.UserID && r.EmojiName == emoji {
			alreadyReacted = true
			break
		}
	}
	if alreadyReacted {
		resp, err := mb.api(ctx, "DELETE", fmt.Sprintf("/users/%s/posts/%s/reactions/%s", mb.UserID, msg.ID, emoji), nil)
		if err != nil {
			return nil, fmt.Errorf("remove reaction: %w", err)
		}
		resp.Body.Close()
	} else {
		if err := mb.apiJSON(ctx, "POST", "/reactions", mmReaction{
			UserID: mb.UserID, PostID: msg.ID, EmojiName: emoji,
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

func (mb *MmBackend) Forward(ctx context.Context, from backend.ChatEntry, to backend.ChatEntry, msgID string) error {
	return fmt.Errorf("forwarding is not supported on Mattermost")
}

func (mb *MmBackend) Pin(ctx context.Context, ch backend.ChatEntry, msg backend.Message, unpin bool) error {
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

func (mb *MmBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
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
	messages := make([]backend.Message, 0, len(result.Order))
	for _, id := range result.Order {
		if p, ok := result.Posts[id]; ok && p.DeleteAt == 0 {
			messages = append(messages, mb.postToMessage(ctx, p))
		}
	}
	return messages, nil
}

// Mentions searches every team for posts naming the user or the whole
// channel, as Mattermost's own Recent Mentions does.
func (mb *MmBackend) Mentions(ctx context.Context, limit int) ([]backend.Mention, error) {
	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil {
		return nil, fmt.Errorf("load teams: %w", err)
	}
	body := map[string]any{
		"terms":        "@" + mb.Username + " @channel @all @here",
		"is_or_search": true,
		"per_page":     limit,
	}
	var posts []mmPost
	for _, team := range teams {
		var result struct {
			Order []string          `json:"order"`
			Posts map[string]mmPost `json:"posts"`
		}
		path := fmt.Sprintf("/teams/%s/posts/search", team.ID)
		if err := mb.apiJSON(ctx, "POST", path, body, &result); err != nil {
			return nil, fmt.Errorf("search mentions: %w", err)
		}
		for _, id := range result.Order {
			if p, ok := result.Posts[id]; ok && p.DeleteAt == 0 && p.UserID != mb.UserID {
				posts = append(posts, p)
			}
		}
	}
	sort.SliceStable(posts, func(i, j int) bool { return posts[i].CreateAt > posts[j].CreateAt })
	teamNames := make(map[string]string, len(teams))
	for _, team := range teams {
		teamNames[team.ID] = team.DisplayName
	}
	channels := make(map[string]*mmChannel)
	var found []backend.Mention
	for _, p := range posts {
		if len(found) == limit {
			break
		}
		ch, ok := channels[p.ChannelID]
		if !ok {
			var c mmChannel
			if err := mb.apiJSON(ctx, "GET", "/channels/"+p.ChannelID, nil, &c); err == nil {
				ch = &c
			}
			channels[p.ChannelID] = ch
		}
		if ch == nil || ch.Type == "D" || ch.Type == "G" {
			continue
		}
		found = append(found, backend.Mention{
			Chat: backend.ChatEntry{
				ID:      backend.ChatID{Backend: "mattermost", Id: ch.ID},
				Title:   mb.channelTitle(ctx, *ch, teamNames[ch.TeamID]),
				IsGroup: true,
				Raw:     *ch,
			},
			Message: mb.postToMessage(ctx, p),
		})
	}
	return found, nil
}

func (mb *MmBackend) teamForChannel(ctx context.Context, ch backend.ChatEntry) string {
	if raw, ok := ch.Raw.(mmChannel); ok && raw.TeamID != "" {
		return raw.TeamID
	}
	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil || len(teams) == 0 {
		return ""
	}
	return teams[0].ID
}

func (mb *MmBackend) SetActive(ch backend.ChatEntry) {
	mb.mu.Lock()
	mb.subscribed = ch.ID.Id
	mb.mu.Unlock()
}

func (mb *MmBackend) Events() *backend.Bus { return &mb.events }

func (mb *MmBackend) IsTyping() bool {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	for uid, t := range mb.typing {
		if uid != mb.UserID && time.Since(t) < 6*time.Second {
			return true
		}
	}
	return false
}

func (mb *MmBackend) ClearTyping() {
	mb.mu.Lock()
	mb.typing = make(map[string]time.Time)
	mb.mu.Unlock()
}

func (mb *MmBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string {
	return ""
}

func (mb *MmBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error {
	return nil
}

func (mb *MmBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	mb.mu.Lock()
	n := mb.unreads[ch.ID.Id]
	mb.mu.Unlock()
	return n, nil
}

func (mb *MmBackend) CachedUnread(ch backend.ChatEntry) int {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return mb.unreads[ch.ID.Id]
}

func (mb *MmBackend) InboxPosition(ch backend.ChatEntry) string {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return mb.lastRead[ch.ID.Id]
}

func (mb *MmBackend) MediaPlayable(msg backend.Message) bool { return false }

func (mb *MmBackend) MediaExec(ctx context.Context, msg backend.Message, player string) (*exec.Cmd, func(), error) {
	return nil, nil, fmt.Errorf("media playback is not supported on Mattermost")
}

func (mb *MmBackend) MediaSaveable(msg backend.Message) (string, bool) {
	p, ok := msg.Raw.(mmPost)
	if !ok {
		return "", false
	}
	if p.Metadata != nil && len(p.Metadata.Files) > 0 {
		return backend.SafeFilename(p.Metadata.Files[0].Name, "attachment"), true
	}
	if len(p.FileIDs) > 0 {
		return "attachment", true
	}
	return "", false
}

func (mb *MmBackend) SaveMedia(ctx context.Context, msg backend.Message, destDir string) error {
	p, ok := msg.Raw.(mmPost)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	var fileID, filename string
	if p.Metadata != nil && len(p.Metadata.Files) > 0 {
		fileID = p.Metadata.Files[0].ID
		filename = backend.SafeFilename(p.Metadata.Files[0].Name, "attachment")
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
	f, err := backend.CreateUnique(destDir, filename)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err := backend.FirstError(copyErr, closeErr); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("save file: %w", err)
	}
	return nil
}

func (mb *MmBackend) AcknowledgeMedia(ctx context.Context, msg backend.Message) {}

func (mb *MmBackend) userCandidate(u mmUser) backend.UserCandidate {
	label := u.displayName()
	if u.Username != "" && !strings.EqualFold(label, u.Username) {
		label += "  @" + u.Username
	}
	match := strings.ToLower(u.Username + " " + u.First + " " + u.Last + " " + u.Nickname)
	return backend.UserCandidate{Key: u.ID, Label: label, Match: match, Raw: u}
}

func (mb *MmBackend) UserSearchBase(ctx context.Context, recentChats []backend.ChatEntry) (backend.UserSearchBase, error) {
	var base []backend.UserCandidate
	for _, ch := range recentChats {
		raw, ok := ch.Raw.(mmChannel)
		if !ok || raw.Type != "D" {
			continue
		}
		for _, uid := range strings.Split(raw.Name, "__") {
			if uid != mb.UserID && uid != "" {
				base = append(base, mb.userCandidate(mb.resolveUser(ctx, uid)))
			}
		}
	}
	channels, err := mb.joinedChannels(ctx)
	local := append(base, channels...)
	return backend.UserSearchBase{Heading: "Recent DMs", Base: base, Local: local}, err
}

// mmChannelChoice is a channel row in the search; one the user has not
// joined is joined when picked, as Mattermost's Browse channels does.
type mmChannelChoice struct {
	ch     mmChannel
	joined bool
}

func (mb *MmBackend) channelCandidate(ch mmChannel, teamName string, joined bool) backend.UserCandidate {
	name := ch.DisplayName
	if name == "" {
		name = ch.Name
	}
	label := name + "  ~" + ch.Name
	if ch.Type == "G" {
		label = name + "  group"
	}
	if teamName != "" {
		label += " · " + teamName
	}
	if !joined {
		label += " · join"
	}
	match := strings.ToLower(ch.Name + " " + ch.DisplayName)
	return backend.UserCandidate{Key: ch.ID, Label: label, Match: match,
		Raw: mmChannelChoice{ch, joined}}
}

func (mb *MmBackend) teams(ctx context.Context) ([]mmTeam, error) {
	var teams []mmTeam
	if err := mb.apiJSON(ctx, "GET", "/users/me/teams", nil, &teams); err != nil {
		return nil, fmt.Errorf("load teams: %w", err)
	}
	return teams, nil
}

// joinedChannels lists every channel and group DM the user is in, for the
// search to filter locally.
func (mb *MmBackend) joinedChannels(ctx context.Context) ([]backend.UserCandidate, error) {
	teams, err := mb.teams(ctx)
	if err != nil {
		return nil, err
	}
	teamNames := make(map[string]string, len(teams))
	for _, team := range teams {
		teamNames[team.ID] = team.DisplayName
	}
	var out []backend.UserCandidate
	for page := 0; ; page++ {
		var batch []mmChannel
		path := fmt.Sprintf("/users/me/channels?page=%d&per_page=200", page)
		if err := mb.apiJSON(ctx, "GET", path, nil, &batch); err != nil {
			return out, fmt.Errorf("load channels: %w", err)
		}
		for _, ch := range batch {
			if ch.Type != "D" {
				out = append(out, mb.channelCandidate(ch, teamNames[ch.TeamID], true))
			}
		}
		if len(batch) < 200 {
			return out, nil
		}
	}
}

// FindPeople searches the server's users; opening one creates the DM channel,
// which shows the other person nothing until a message is sent.
func (mb *MmBackend) FindPeople(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	var users []mmUser
	if err := mb.apiJSON(ctx, "POST", "/users/search", map[string]string{"term": query}, &users); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	var out []backend.UserCandidate
	for _, u := range users {
		if u.ID != mb.UserID {
			out = append(out, mb.userCandidate(u))
		}
	}
	return out, nil
}

func (mb *MmBackend) UserSearchRemote(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	out, err := mb.FindPeople(ctx, query)
	if err != nil {
		return nil, err
	}
	// Public channels in the user's teams; joined ones are already listed
	// locally under the same key, so these rows are the ones to join.
	teams, err := mb.teams(ctx)
	if err != nil {
		return out, err
	}
	for _, team := range teams {
		var channels []mmChannel
		path := fmt.Sprintf("/teams/%s/channels/search", team.ID)
		if err := mb.apiJSON(ctx, "POST", path, map[string]string{"term": query}, &channels); err != nil {
			return out, fmt.Errorf("search channels: %w", err)
		}
		for _, ch := range channels {
			out = append(out, mb.channelCandidate(ch, team.DisplayName, false))
		}
	}
	return out, nil
}

func (mb *MmBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	switch raw := c.Raw.(type) {
	case mmUser:
		return mb.openDM(ctx, raw)
	case mmChannelChoice:
		return mb.openChannel(ctx, raw)
	}
	return backend.ChatEntry{}, fmt.Errorf("open chat: unexpected search result")
}

func (mb *MmBackend) openChannel(ctx context.Context, choice mmChannelChoice) (backend.ChatEntry, error) {
	ch := choice.ch
	if !choice.joined {
		path := "/channels/" + ch.ID + "/members"
		if err := mb.apiJSON(ctx, "POST", path, map[string]string{"user_id": mb.UserID}, nil); err != nil {
			return backend.ChatEntry{}, fmt.Errorf("join channel %s: %w", ch.DisplayName, err)
		}
	}
	teamName := ""
	if teams, err := mb.teams(ctx); err == nil {
		for _, team := range teams {
			if team.ID == ch.TeamID {
				teamName = team.DisplayName
			}
		}
	}
	isDM := ch.Type == "G"
	return backend.ChatEntry{
		ID:      backend.ChatID{Backend: "mattermost", Id: ch.ID},
		Title:   mb.channelTitle(ctx, ch, teamName),
		IsDM:    isDM,
		IsGroup: !isDM,
		Raw:     ch,
	}, nil
}

// CanMention is true for channels and group DMs; a DM has no one else to name.
func (mb *MmBackend) CanMention(ch backend.ChatEntry) bool {
	raw, ok := ch.Raw.(mmChannel)
	return ok && raw.Type != "D"
}

func (mb *MmBackend) MentionBase(ctx context.Context, ch backend.ChatEntry) (backend.UserSearchBase, error) {
	found, err := mb.MentionSearch(ctx, ch, "")
	return backend.UserSearchBase{Heading: "Channel members", Base: found, Local: found}, err
}

// MentionSearch lists the channel's members first, then people outside it,
// as the Mattermost web client's autocomplete does.
func (mb *MmBackend) MentionSearch(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.UserCandidate, error) {
	raw, _ := ch.Raw.(mmChannel)
	q := url.Values{"in_channel": {raw.ID}, "name": {query}, "limit": {"50"}}
	if raw.TeamID != "" {
		q.Set("in_team", raw.TeamID)
	}
	var resp struct {
		Users        []mmUser `json:"users"`
		OutOfChannel []mmUser `json:"out_of_channel"`
	}
	if err := mb.apiJSON(ctx, "GET", "/users/autocomplete?"+q.Encode(), nil, &resp); err != nil {
		return nil, fmt.Errorf("search channel members: %w", err)
	}
	var out []backend.UserCandidate
	add := func(users []mmUser, tag string) {
		for _, u := range users {
			if u.ID == mb.UserID || u.Username == "" {
				continue
			}
			c := mb.userCandidate(u)
			c.Label += tag
			c.Mention = "@" + u.Username + " "
			out = append(out, c)
		}
	}
	add(resp.Users, "")
	add(resp.OutOfChannel, "  · not in channel")
	return out, nil
}

func (mb *MmBackend) openDM(ctx context.Context, user mmUser) (backend.ChatEntry, error) {
	var ch mmChannel
	if err := mb.apiJSON(ctx, "POST", "/channels/direct", []string{mb.UserID, user.ID}, &ch); err != nil {
		return backend.ChatEntry{}, fmt.Errorf("open DM with %s: %w", user.displayName(), err)
	}
	mb.mu.Lock()
	mb.userCache[user.ID] = user
	mb.mu.Unlock()
	return backend.ChatEntry{
		ID:    backend.ChatID{Backend: "mattermost", Id: ch.ID},
		Title: user.displayName(),
		IsDM:  true,
		Raw:   ch,
	}, nil
}

func (mb *MmBackend) RecentEmoji() []string { return mb.recentEmoji }

func (mb *MmBackend) RememberEmoji(value string) {
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

func (mb *MmBackend) EmojiChoices(query string) []backend.EmojiOption {
	catalog := backend.EmojiCatalog()
	if query == "" {
		return backend.RecentFirst(catalog, mb.recentEmoji)
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

func (mb *MmBackend) ReactionChoices(query string) []backend.EmojiOption {
	return mb.EmojiChoices(query)
}

func (mb *MmBackend) StartWebSocket(ctx context.Context) {
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

func (mb *MmBackend) runWebSocket(ctx context.Context) error {
	wsURL := strings.Replace(mb.ServerURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/api/v4/websocket"

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	// A long post with its metadata easily passes the library's 32 KiB default,
	// and going over it drops the connection.
	conn.SetReadLimit(4 << 20)
	mb.wsConn = conn

	auth := map[string]any{
		"seq":    1,
		"action": "authentication_challenge",
		"data":   map[string]string{"token": mb.Token},
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

func (mb *MmBackend) handleWSEvent(data []byte) {
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

	chat := backend.ChatID{Backend: "mattermost", Id: channelID}
	switch event.Event {
	case "posted":
		var wrapper struct {
			Post        string `json:"post"`
			ChannelType string `json:"channel_type"`
			ChannelName string `json:"channel_name"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			return
		}
		var post mmPost
		if err := json.Unmarshal([]byte(wrapper.Post), &post); err != nil {
			return
		}
		if post.ChannelID != subscribed && post.UserID != mb.UserID {
			mb.mu.Lock()
			mb.unreads[post.ChannelID]++
			mb.mu.Unlock()
		}
		// A "D" channel named after the user twice is the self DM. Posts from
		// other bots are left out; a bot's own, on its own connection, are not.
		dm := wrapper.ChannelType == "D" &&
			(post.UserID == mb.UserID || post.Props["from_bot"] != "true") &&
			wrapper.ChannelName != mb.UserID+"__"+mb.UserID
		msg := mb.postToMessage(context.Background(), post)
		mb.events.Publish(backend.Event{
			Kind: backend.EventMessage, Chat: backend.ChatID{Backend: "mattermost", Id: post.ChannelID},
			DM: dm, Message: &msg,
		})

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
		// The edit event does not carry the channel type, so it is never
		// marked DM.
		msg := mb.postToMessage(context.Background(), post)
		mb.events.Publish(backend.Event{
			Kind: backend.EventEdit, Chat: backend.ChatID{Backend: "mattermost", Id: post.ChannelID},
			Message: &msg,
		})

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
		mb.events.Publish(backend.Event{
			Kind: backend.EventDelete, Chat: backend.ChatID{Backend: "mattermost", Id: post.ChannelID},
			Deleted: []string{post.ID},
		})

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
		// Aggregate here, on the websocket goroutine, never on the UI's.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var all []mmReaction
		mb.apiJSON(ctx, "GET", "/posts/"+reaction.PostID+"/reactions", nil, &all)
		cancel()
		mb.events.Publish(backend.Event{
			Kind: backend.EventReaction, Chat: chat,
			Reaction: &backend.GenericReactionUpdate{
				MessageID: reaction.PostID, Reactions: mb.aggregateReactions(all),
			},
		})

	case "typing":
		if event.Broadcast.UserID == mb.UserID {
			return
		}
		if channelID == subscribed {
			mb.mu.Lock()
			mb.typing[event.Broadcast.UserID] = time.Now()
			mb.mu.Unlock()
		}
		mb.events.Publish(backend.Event{Kind: backend.EventTyping, Chat: chat})
	}
}

// Profile describes a user from /users/{id} and their status.
func (mb *MmBackend) Profile(ctx context.Context, userID string) (backend.Profile, error) {
	var u struct {
		mmUser
		Email          string            `json:"email"`
		Position       string            `json:"position"`
		Locale         string            `json:"locale"`
		IsBot          bool              `json:"is_bot"`
		BotDescription string            `json:"bot_description"`
		Props          map[string]string `json:"props"`
		Timezone       struct {
			Automatic   string `json:"useAutomaticTimezone"`
			AutomaticTZ string `json:"automaticTimezone"`
			ManualTZ    string `json:"manualTimezone"`
		} `json:"timezone"`
	}
	if err := mb.apiJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, &u); err != nil {
		return backend.Profile{}, fmt.Errorf("load profile: %w", err)
	}
	p := backend.Profile{Name: u.displayName()}
	p.Add("Username", "@"+u.Username)
	if full := strings.TrimSpace(u.First + " " + u.Last); full != p.Name {
		p.Add("Full name", full)
	}
	p.Add("Position", u.Position)
	p.Add("Email", u.Email)
	if u.IsBot {
		p.Add("Bot", cmp.Or(u.BotDescription, "yes"))
	}
	var custom struct {
		Emoji string `json:"emoji"`
		Text  string `json:"text"`
	}
	if raw := u.Props["customStatus"]; raw != "" && json.Unmarshal([]byte(raw), &custom) == nil {
		if custom.Emoji != "" {
			custom.Text = reactionLabel(custom.Emoji) + " " + custom.Text
		}
		p.Add("Custom status", custom.Text)
	}
	var status struct {
		Status       string `json:"status"`
		LastActivity int64  `json:"last_activity_at"`
	}
	if mb.apiJSON(ctx, "GET", "/users/"+url.PathEscape(userID)+"/status", nil, &status) == nil {
		p.Add("Status", status.Status)
		if status.Status != "online" && status.LastActivity > 0 {
			p.Add("Last active", time.UnixMilli(status.LastActivity).Local().
				Format("2 Jan 2006 15:04"))
		}
	}
	tz := u.Timezone.ManualTZ
	if u.Timezone.Automatic == "true" {
		tz = u.Timezone.AutomaticTZ
	}
	if loc, err := time.LoadLocation(tz); tz != "" && err == nil {
		p.Add("Local time", time.Now().In(loc).Format("15:04")+" ("+tz+")")
	}
	p.Add("Locale", u.Locale)
	return p, nil
}
