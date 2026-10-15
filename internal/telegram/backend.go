package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/tg"

	"wiregram/internal/backend"
	"wiregram/internal/config"
)

type TelegramBackend struct {
	C *Client
}

func peerKeyToID(key peerKey) backend.ChatID {
	return backend.ChatID{Backend: "telegram", Id: fmt.Sprintf("%c%d", key.kind, key.id)}
}

func chatToChatEntry(ch chat) backend.ChatEntry {
	_, isSelf := ch.peer.(*tg.InputPeerSelf)
	return backend.ChatEntry{
		ID:      peerKeyToID(ch.key),
		Title:   ch.title,
		Unread:  ch.unread,
		IsDM:    ch.key.kind == 'u',
		IsGroup: ch.key.kind == 'g' || ch.key.kind == 'c',
		IsSelf:  isSelf,
		Raw:     ch,
	}
}

func entryToChat(entry backend.ChatEntry) chat {
	if ch, ok := entry.Raw.(chat); ok {
		return ch
	}
	return chat{}
}

func chatsToChatEntries(chats []chat) []backend.ChatEntry {
	entries := make([]backend.ChatEntry, len(chats))
	for i, ch := range chats {
		entries[i] = chatToChatEntry(ch)
	}
	return entries
}

func chatEntriesToChats(entries []backend.ChatEntry) []chat {
	chats := make([]chat, len(entries))
	for i, e := range entries {
		chats[i] = entryToChat(e)
	}
	return chats
}

func tgToMessage(c *Client, m *tg.Message) backend.Message {
	sender := ""
	senderID := ""
	if !m.Out {
		peer := m.FromID
		if peer == nil {
			peer = m.PeerID
		}
		c.mu.Lock()
		switch p := peer.(type) {
		case *tg.PeerUser:
			u := c.users[p.UserID]
			sender = UserName(u)
			if u != nil && u.Self {
				sender = "you"
			}
			senderID = strconv.FormatInt(p.UserID, 10)
		default:
			sender = c.chats[keyOf(peer)]
		}
		c.mu.Unlock()
	}

	var editDate int
	if m.EditDate != 0 {
		editDate = m.EditDate
	}
	_, forwarded := m.GetFwdFrom()

	var reactions []backend.MessageReaction
	if r, ok := m.GetReactions(); ok {
		for _, count := range r.Results {
			label := reactionLabel(count.Reaction)
			if label == "" {
				continue
			}
			_, chosen := count.GetChosenOrder()
			reactions = append(reactions, backend.MessageReaction{
				Emoji: label, Count: count.Count, Chosen: chosen,
			})
		}
	}

	return backend.Message{
		ID:         strconv.Itoa(m.ID),
		Sender:     sender,
		SenderID:   senderID,
		Text:       m.Message,
		Timestamp:  time.Unix(int64(m.Date), 0).Local(),
		Outgoing:   m.Out,
		Pinned:     m.Pinned,
		EditDate:   editDate,
		ReplyToID:  tgReplyToID(m),
		Forwarded:  forwarded,
		ViaBot:     m.ViaBotID != 0,
		MediaLabel: formatMedia(m.Media),
		Image:      inlineImage(m.Media),
		Multiline:  len(m.Message) > 0 && (m.Message[0] == '\n' || m.Message[len(m.Message)-1] == '\n' || containsNewline(m.Message)),
		Reactions:  reactions,
		Preview:    webPagePreview(m.Media),
		Spans:      tgSpans(m.Message, m.Entities),
		Mentioned:  !m.Out && c.mentionsMe(m),
		Raw:        m,
	}
}

// mentionsMe reports whether m names the user in a mention entity. The
// server's Mentioned flag is not enough: it is also set on replies to them.
func (c *Client) mentionsMe(m *tg.Message) bool {
	if len(m.Entities) == 0 {
		return false
	}
	c.mu.Lock()
	username := ""
	if u := c.users[c.AccountID]; u != nil {
		username = u.Username
	}
	c.mu.Unlock()
	var units []uint16
	for _, e := range m.Entities {
		switch e := e.(type) {
		case *tg.MessageEntityMentionName:
			if e.UserID == c.AccountID {
				return true
			}
		case *tg.MessageEntityMention:
			if username == "" {
				continue
			}
			if units == nil {
				units = utf16.Encode([]rune(m.Message))
			}
			start, end := e.Offset, e.Offset+e.Length
			if start < 0 || end > len(units) || start >= end {
				continue
			}
			name := string(utf16.Decode(units[start:end]))
			if strings.EqualFold(strings.TrimPrefix(name, "@"), username) {
				return true
			}
		}
	}
	return false
}

// tgSpans turns a message's entities into spans. Entities count UTF-16 code
// units, spans count bytes.
func tgSpans(text string, entities []tg.MessageEntityClass) []backend.Span {
	if len(entities) == 0 {
		return nil
	}
	// at[u] is the byte offset of UTF-16 unit u.
	at := make([]int, 0, len(text)+1)
	for i, r := range text {
		at = append(at, i)
		if r >= 0x10000 {
			at = append(at, i)
		}
	}
	at = append(at, len(text))
	var spans []backend.Span
	for _, e := range entities {
		var style backend.Style
		url := ""
		switch e := e.(type) {
		case *tg.MessageEntityBold:
			style = backend.StyleBold
		case *tg.MessageEntityItalic:
			style = backend.StyleItalic
		case *tg.MessageEntityStrike:
			style = backend.StyleStrike
		case *tg.MessageEntityCode, *tg.MessageEntityPre:
			style = backend.StyleCode
		case *tg.MessageEntityTextURL:
			style, url = backend.StyleLink, e.URL
		default:
			continue
		}
		start, end := e.GetOffset(), e.GetOffset()+e.GetLength()
		if start < 0 || end > len(at)-1 || start >= end {
			continue
		}
		spans = append(spans, backend.Span{Start: at[start], End: at[end], Style: style, URL: url})
	}
	return spans
}

func tgReplyToID(m *tg.Message) string {
	reply, ok := m.GetReplyTo()
	if !ok {
		return ""
	}
	if header, ok := reply.(*tg.MessageReplyHeader); ok {
		return strconv.Itoa(header.ReplyToMsgID)
	}
	return ""
}

func containsNewline(s string) bool {
	for _, c := range s {
		if c == '\n' || c == '\r' {
			return true
		}
	}
	return false
}

func tgMessagesToMessages(c *Client, msgs []*tg.Message) []backend.Message {
	result := make([]backend.Message, len(msgs))
	for i, m := range msgs {
		result[i] = tgToMessage(c, m)
	}
	return result
}

func tgReactionsToMessageReactions(reactions tg.MessageReactions) []backend.MessageReaction {
	result := make([]backend.MessageReaction, 0, len(reactions.Results))
	for _, count := range reactions.Results {
		label := reactionLabel(count.Reaction)
		if label == "" {
			continue
		}
		_, chosen := count.GetChosenOrder()
		result = append(result, backend.MessageReaction{Emoji: label, Count: count.Count, Chosen: chosen})
	}
	return result
}

func msgIDToInt(id string) int {
	n, _ := strconv.Atoi(id)
	return n
}

func (tb *TelegramBackend) Name() string { return "telegram" }

func (tb *TelegramBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		Forward: true, DeleteForMe: true, OutboxReceipt: true,
		ViewOnce: true, Reactions: true, Edit: true, Pin: true,
		Upload: true, Search: true, Typing: true, DeleteChat: true, Archive: true,
	}
}

func (tb *TelegramBackend) Run(ctx context.Context) error {
	return tb.C.Telegram.Run(ctx, func(runCtx context.Context) error {
		return nil
	})
}

func (tb *TelegramBackend) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	return tb.C.Authenticate(ctx, prompt, report)
}

func (tb *TelegramBackend) Identity() backend.AccountIdentity {
	return backend.AccountIdentity{ID: tb.C.AccountID, Name: tb.C.AccountName}
}

func (tb *TelegramBackend) AccountLabel() string { return "[TG] " + tb.C.AccountName }

func (tb *TelegramBackend) Logout(ctx context.Context) error {
	if _, err := tb.C.Api.AuthLogOut(ctx); err != nil {
		return fmt.Errorf("log out of Telegram: %w", err)
	}
	return nil
}

func (tb *TelegramBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	chats, archiveUnread, err := tb.C.dialogs(ctx, archived)
	if err != nil {
		return nil, 0, err
	}
	return chatsToChatEntries(chats), archiveUnread, nil
}

func (tb *TelegramBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	msgs, err := tb.C.history(ctx, entryToChat(ch))
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.C, msgs), nil
}

func (tb *TelegramBackend) UnreadMessages(ctx context.Context, ch backend.ChatEntry, limit int) ([]backend.Message, error) {
	msgs, err := tb.C.unreadHistory(ctx, entryToChat(ch), limit)
	if err != nil {
		return nil, err
	}
	return backend.NewestIncoming(tgMessagesToMessages(tb.C, msgs), limit), nil
}

func (tb *TelegramBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	msgs, err := tb.C.olderHistory(ctx, entryToChat(ch), msgIDToInt(beforeID))
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.C, msgs), nil
}

func (tb *TelegramBackend) Messages(ctx context.Context, ch backend.ChatEntry, ids []string) ([]backend.Message, error) {
	nums := make([]int, 0, len(ids))
	for _, id := range ids {
		if n := msgIDToInt(id); n > 0 {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return nil, nil
	}
	msgs, err := tb.C.messagesByID(ctx, entryToChat(ch), nums)
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.C, msgs), nil
}

func (tb *TelegramBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	m, err := tb.C.send(ctx, entryToChat(ch), text, msgIDToInt(replyTo))
	if err != nil {
		return backend.Message{}, err
	}
	return tgToMessage(tb.C, m), nil
}

func (tb *TelegramBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, path, caption, replyTo string) (backend.Message, error) {
	m, err := tb.C.sendUpload(ctx, entryToChat(ch), path, caption, msgIDToInt(replyTo))
	if err != nil {
		return backend.Message{}, err
	}
	return tgToMessage(tb.C, m), nil
}

func (tb *TelegramBackend) UploadPlaceholder(path, caption string) backend.Message {
	info, err := os.Stat(path)
	if err != nil {
		return backend.Message{Outgoing: true, Timestamp: time.Now(), MediaLabel: "[upload]", Text: caption}
	}
	m := uploadPlaceholder(path, caption, info.Size())
	return tgToMessage(tb.C, m)
}

func (tb *TelegramBackend) Editable(msg backend.Message) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	return editableMessage(m)
}

func (tb *TelegramBackend) Edit(ctx context.Context, ch backend.ChatEntry, msg backend.Message, text string) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("this message cannot be edited")
	}
	return tb.C.editMessage(ctx, entryToChat(ch), m, text)
}

func (tb *TelegramBackend) DeleteMessages(ctx context.Context, ch backend.ChatEntry, ids []string, forEveryone bool) error {
	intIDs := make([]int, len(ids))
	for i, id := range ids {
		intIDs[i] = msgIDToInt(id)
	}
	return tb.C.deleteMessages(ctx, entryToChat(ch), intIDs, forEveryone)
}

func (tb *TelegramBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return tb.C.deleteChat(ctx, entryToChat(ch), forEveryone)
}

func (tb *TelegramBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	return tb.C.markRead(ctx, entryToChat(ch), msgIDToInt(upToID))
}

func (tb *TelegramBackend) SetTyping(ctx context.Context, ch backend.ChatEntry) {
	entry := entryToChat(ch)
	tb.C.Api.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{
		Peer:   entry.peer,
		Action: &tg.SendMessageTypingAction{},
	})
}

func (tb *TelegramBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("select a sent message before reacting")
	}
	reactions, err := tb.C.sendReaction(ctx, entryToChat(ch), m, emoji)
	if err != nil {
		return nil, err
	}
	return tgReactionsToMessageReactions(reactions), nil
}

func (tb *TelegramBackend) Forward(ctx context.Context, from backend.ChatEntry, to backend.ChatEntry, msgID string) error {
	fromChat := entryToChat(from)
	toChat := entryToChat(to)
	return tb.C.forwardMessage(ctx, fromChat.peer, toChat.peer, msgIDToInt(msgID))
}

func (tb *TelegramBackend) Pin(ctx context.Context, ch backend.ChatEntry, msg backend.Message, unpin bool) error {
	entry := entryToChat(ch)
	request := &tg.MessagesUpdatePinnedMessageRequest{
		Peer:  entry.peer,
		ID:    msgIDToInt(msg.ID),
		Unpin: unpin,
	}
	_, err := tb.C.Api.MessagesUpdatePinnedMessage(ctx, request)
	if err != nil {
		return fmt.Errorf("pin message: %w", err)
	}
	return nil
}

func (tb *TelegramBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
	msgs, err := tb.C.searchMessages(ctx, entryToChat(ch), query, &tg.InputMessagesFilterEmpty{}, 20)
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.C, msgs), nil
}

// mentionChats bounds how many recent groups Mentions asks, one request
// each, so a long chat list does not run into flood waits.
const mentionChats = 20

// Mentions asks each recent group for the messages the server files as
// mentions, keeping those that name the user rather than reply to them.
func (tb *TelegramBackend) Mentions(ctx context.Context, limit int) ([]backend.Mention, error) {
	chats, _, err := tb.C.dialogs(ctx, false)
	if err != nil {
		return nil, err
	}
	var found []backend.Mention
	asked := 0
	for _, ch := range chats {
		if ch.key.kind == 'u' || ch.broadcast || asked == mentionChats {
			continue
		}
		asked++
		msgs, err := tb.C.searchMessages(ctx, ch, "", &tg.InputMessagesFilterMyMentions{}, 3)
		if err != nil {
			return found, err
		}
		entry := chatToChatEntry(ch)
		for _, m := range msgs {
			if msg := tgToMessage(tb.C, m); msg.Mentioned {
				found = append(found, backend.Mention{Chat: entry, Message: msg})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].Message.Timestamp.After(found[j].Message.Timestamp)
	})
	return found[:min(len(found), limit)], nil
}

func (tb *TelegramBackend) SetActive(ch backend.ChatEntry) {
	if ch.ID == (backend.ChatID{}) {
		tb.C.setActive(peerKey{})
		return
	}
	tb.C.setActive(entryToChat(ch).key)
}

func (tb *TelegramBackend) Events() *backend.Bus { return &tb.C.events }
func (tb *TelegramBackend) IsTyping() bool       { return tb.C.isTyping() }
func (tb *TelegramBackend) ClearTyping()         { tb.C.clearTyping() }

func (tb *TelegramBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return ""
	}
	return tb.C.readMarker(entryToChat(ch), m)
}

func (tb *TelegramBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error {
	return tb.C.refreshReadState(ctx, entryToChat(ch))
}

func (tb *TelegramBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	return tb.C.refreshChatUnread(ctx, entryToChat(ch))
}

func (tb *TelegramBackend) CachedUnread(ch backend.ChatEntry) int {
	return tb.C.cachedChatUnread(entryToChat(ch).key)
}

func (tb *TelegramBackend) InboxPosition(ch backend.ChatEntry) string {
	state := tb.C.readState(entryToChat(ch).key)
	return strconv.Itoa(state.inbox)
}

func (tb *TelegramBackend) MediaPlayable(msg backend.Message) bool {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return false
	}
	_, err := mediaAttachment(m)
	return err == nil
}

func (tb *TelegramBackend) MediaExec(ctx context.Context, msg backend.Message, player string) (*exec.Cmd, func(), error) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return nil, nil, fmt.Errorf("this message has no playable attachment")
	}
	a, err := mediaAttachment(m)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "wiregram-media-")
	if err != nil {
		return nil, nil, fmt.Errorf("create private media directory: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "attachment")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("create temporary media file: %w", err)
	}
	dlErr := tb.C.downloadMedia(ctx, a, file)
	closeErr := file.Close()
	if err := errors.Join(dlErr, closeErr); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("download media: %w", err)
	}
	return backend.MpvCmd(ctx, player, a.audio, path), cleanup, nil
}

func (tb *TelegramBackend) MediaSaveable(msg backend.Message) (string, bool) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return "", false
	}
	dl, err := mediaDownloadable(m)
	if err != nil {
		return "", false
	}
	return dl.filename, true
}

func (tb *TelegramBackend) SaveMedia(ctx context.Context, msg backend.Message, destDir string) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	dl, err := mediaDownloadable(m)
	if err != nil {
		return err
	}
	return tb.C.saveMedia(ctx, dl, filepath.Join(destDir, dl.filename))
}

func (tb *TelegramBackend) AcknowledgeMedia(ctx context.Context, msg backend.Message) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return
	}
	tb.C.acknowledgeMedia(ctx, m)
}

func (tb *TelegramBackend) UserSearchBase(ctx context.Context, recentChats []backend.ChatEntry) (backend.UserSearchBase, error) {
	c := tb.C
	chats := chatEntriesToChats(recentChats)
	contacts, contactsErr := c.loadContacts(ctx)
	frequent, frequentErr := c.loadFrequentUsers(ctx)
	local := mergeChoices(choicesFromUsers(contacts, "contact"), c.recentUserChoices(chats, "recent"))
	base := choicesFromUsers(frequent, "")
	heading := "Frequently contacted"
	if len(base) == 0 {
		base, heading = c.recentUserChoices(chats, ""), "Recent"
	}
	err := contactsErr
	if err == nil {
		err = frequentErr
	}
	return backend.UserSearchBase{Heading: heading, Base: tgCandidates(base), Local: tgCandidates(local)}, err
}

func tgCandidates(choices []userChoice) []backend.UserCandidate {
	out := make([]backend.UserCandidate, len(choices))
	for i, choice := range choices {
		key := fmt.Sprintf("%c%d", choice.entry.key.kind, choice.entry.key.id)
		out[i] = backend.UserCandidate{Key: key, Label: choice.label, Match: choice.match, Raw: choice}
		if choice.username != "" {
			out[i].Mention = "@" + choice.username + " "
		}
	}
	return out
}

func (tb *TelegramBackend) UserSearchRemote(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	choices, err := tb.C.remoteUserChoices(ctx, query)
	return tgCandidates(choices), err
}

func (tb *TelegramBackend) FindPeople(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	return tb.UserSearchRemote(ctx, query)
}

func (tb *TelegramBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	choice, ok := c.Raw.(userChoice)
	if !ok {
		return backend.ChatEntry{}, fmt.Errorf("open chat: unexpected search result")
	}
	entry, err := tb.C.resolveUserChoice(ctx, choice)
	if err != nil {
		return backend.ChatEntry{}, err
	}
	return chatToChatEntry(entry), nil
}

// CanMention is true for groups: Telegram mentions people by username there.
func (tb *TelegramBackend) CanMention(ch backend.ChatEntry) bool {
	entry := entryToChat(ch)
	return (entry.key.kind == 'g' || entry.key.kind == 'c') && !entry.broadcast
}

func (tb *TelegramBackend) MentionBase(ctx context.Context, ch backend.ChatEntry) (backend.UserSearchBase, error) {
	choices, err := tb.C.mentionChoices(ctx, entryToChat(ch), "")
	found := tgCandidates(choices)
	return backend.UserSearchBase{Heading: "Group members", Base: found, Local: found}, err
}

func (tb *TelegramBackend) MentionSearch(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.UserCandidate, error) {
	choices, err := tb.C.mentionChoices(ctx, entryToChat(ch), query)
	return tgCandidates(choices), err
}

func (tb *TelegramBackend) RecentEmoji() []string      { return tb.C.recentEmoji() }
func (tb *TelegramBackend) RememberEmoji(value string) { tb.C.rememberEmoji(value) }
func (tb *TelegramBackend) EmojiChoices(query string) []backend.EmojiOption {
	return tb.C.emojiChoices(query)
}
func (tb *TelegramBackend) ReactionChoices(query string) []backend.EmojiOption {
	return tb.C.reactionChoices(query)
}
