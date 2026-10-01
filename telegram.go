package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
)

type telegramBackend struct {
	c *client
}

func peerKeyToID(key peerKey) chatID {
	return chatID{backend: "telegram", id: fmt.Sprintf("%c%d", key.kind, key.id)}
}

func chatToChatEntry(ch chat) chatEntry {
	_, isSelf := ch.peer.(*tg.InputPeerSelf)
	return chatEntry{
		ID:      peerKeyToID(ch.key),
		Title:   ch.title,
		Unread:  ch.unread,
		IsDM:    ch.key.kind == 'u',
		IsGroup: ch.key.kind == 'g' || ch.key.kind == 'c',
		IsSelf:  isSelf,
		Raw:     ch,
	}
}

func entryToChat(entry chatEntry) chat {
	if ch, ok := entry.Raw.(chat); ok {
		return ch
	}
	return chat{}
}

func chatsToChatEntries(chats []chat) []chatEntry {
	entries := make([]chatEntry, len(chats))
	for i, ch := range chats {
		entries[i] = chatToChatEntry(ch)
	}
	return entries
}

func chatEntriesToChats(entries []chatEntry) []chat {
	chats := make([]chat, len(entries))
	for i, e := range entries {
		chats[i] = entryToChat(e)
	}
	return chats
}

func tgToMessage(c *client, m *tg.Message) message {
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
			sender = userName(u)
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

	var reactions []messageReaction
	if r, ok := m.GetReactions(); ok {
		for _, count := range r.Results {
			label := reactionLabel(count.Reaction)
			if label == "" {
				continue
			}
			_, chosen := count.GetChosenOrder()
			reactions = append(reactions, messageReaction{
				Emoji: label, Count: count.Count, Chosen: chosen,
			})
		}
	}

	return message{
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
		Multiline:  len(m.Message) > 0 && (m.Message[0] == '\n' || m.Message[len(m.Message)-1] == '\n' || containsNewline(m.Message)),
		Reactions:  reactions,
		Raw:        m,
	}
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

func tgMessagesToMessages(c *client, msgs []*tg.Message) []message {
	result := make([]message, len(msgs))
	for i, m := range msgs {
		result[i] = tgToMessage(c, m)
	}
	return result
}

func tgReactionsToMessageReactions(reactions tg.MessageReactions) []messageReaction {
	result := make([]messageReaction, 0, len(reactions.Results))
	for _, count := range reactions.Results {
		label := reactionLabel(count.Reaction)
		if label == "" {
			continue
		}
		_, chosen := count.GetChosenOrder()
		result = append(result, messageReaction{Emoji: label, Count: count.Count, Chosen: chosen})
	}
	return result
}

func msgIDToInt(id string) int {
	n, _ := strconv.Atoi(id)
	return n
}

func (tb *telegramBackend) Name() string { return "telegram" }

func (tb *telegramBackend) Capabilities() capabilities {
	return capabilities{
		Forward: true, DeleteForMe: true, OutboxReceipt: true,
		ViewOnce: true, Reactions: true, Edit: true, Pin: true,
		Upload: true, Search: true, Typing: true, DeleteChat: true,
	}
}

func (tb *telegramBackend) Run(ctx context.Context) error {
	return tb.c.telegram.Run(ctx, func(runCtx context.Context) error {
		return nil
	})
}

func (tb *telegramBackend) Authenticate(ctx context.Context, prompt promptFunc, report func(error)) error {
	return tb.c.authenticate(ctx, prompt, report)
}

func (tb *telegramBackend) Identity() accountIdentity {
	return accountIdentity{ID: tb.c.accountID, Name: tb.c.accountName}
}

func (tb *telegramBackend) AccountLabel() string { return tb.c.accountName }

func (tb *telegramBackend) AutoLockEnabled() bool { return tb.c.autoLock }

func (tb *telegramBackend) Dialogs(ctx context.Context, archived bool) ([]chatEntry, int, error) {
	chats, archiveUnread, err := tb.c.dialogs(ctx, archived)
	if err != nil {
		return nil, 0, err
	}
	return chatsToChatEntries(chats), archiveUnread, nil
}

func (tb *telegramBackend) History(ctx context.Context, ch chatEntry) ([]message, error) {
	msgs, err := tb.c.history(ctx, entryToChat(ch))
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.c, msgs), nil
}

func (tb *telegramBackend) OlderHistory(ctx context.Context, ch chatEntry, beforeID string) ([]message, error) {
	msgs, err := tb.c.olderHistory(ctx, entryToChat(ch), msgIDToInt(beforeID))
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.c, msgs), nil
}

func (tb *telegramBackend) Send(ctx context.Context, ch chatEntry, text string, replyTo string) (message, error) {
	m, err := tb.c.send(ctx, entryToChat(ch), text, msgIDToInt(replyTo))
	if err != nil {
		return message{}, err
	}
	return tgToMessage(tb.c, m), nil
}

func (tb *telegramBackend) SendUpload(ctx context.Context, ch chatEntry, path, caption, replyTo string) (message, error) {
	m, err := tb.c.sendUpload(ctx, entryToChat(ch), path, caption, msgIDToInt(replyTo))
	if err != nil {
		return message{}, err
	}
	return tgToMessage(tb.c, m), nil
}

func (tb *telegramBackend) UploadPlaceholder(path, caption string) message {
	info, err := os.Stat(path)
	if err != nil {
		return message{Outgoing: true, Timestamp: time.Now(), MediaLabel: "[upload]", Text: caption}
	}
	m := uploadPlaceholder(path, caption, info.Size())
	return tgToMessage(tb.c, m)
}

func (tb *telegramBackend) Editable(msg message) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	return editableMessage(m)
}

func (tb *telegramBackend) Edit(ctx context.Context, ch chatEntry, msg message, text string) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("this message cannot be edited")
	}
	return tb.c.editMessage(ctx, entryToChat(ch), m, text)
}

func (tb *telegramBackend) DeleteMessages(ctx context.Context, ch chatEntry, ids []string, forEveryone bool) error {
	intIDs := make([]int, len(ids))
	for i, id := range ids {
		intIDs[i] = msgIDToInt(id)
	}
	return tb.c.deleteMessages(ctx, entryToChat(ch), intIDs, forEveryone)
}

func (tb *telegramBackend) DeleteChat(ctx context.Context, ch chatEntry, forEveryone bool) error {
	return tb.c.deleteChat(ctx, entryToChat(ch), forEveryone)
}

func (tb *telegramBackend) MarkRead(ctx context.Context, ch chatEntry, upToID string) error {
	return tb.c.markRead(ctx, entryToChat(ch), msgIDToInt(upToID))
}

func (tb *telegramBackend) SetTyping(ctx context.Context, ch chatEntry) {
	entry := entryToChat(ch)
	tb.c.api.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{
		Peer:   entry.peer,
		Action: &tg.SendMessageTypingAction{},
	})
}

func (tb *telegramBackend) React(ctx context.Context, ch chatEntry, msg message, emoji string) ([]messageReaction, error) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("select a sent message before reacting")
	}
	reactions, err := tb.c.sendReaction(ctx, entryToChat(ch), m, emoji)
	if err != nil {
		return nil, err
	}
	return tgReactionsToMessageReactions(reactions), nil
}

func (tb *telegramBackend) Forward(ctx context.Context, from chatEntry, to chatEntry, msgID string) error {
	fromChat := entryToChat(from)
	toChat := entryToChat(to)
	return tb.c.forwardMessage(ctx, fromChat.peer, toChat.peer, msgIDToInt(msgID))
}

func (tb *telegramBackend) Pin(ctx context.Context, ch chatEntry, msg message, unpin bool) error {
	entry := entryToChat(ch)
	request := &tg.MessagesUpdatePinnedMessageRequest{
		Peer:  entry.peer,
		ID:    msgIDToInt(msg.ID),
		Unpin: unpin,
	}
	_, err := tb.c.api.MessagesUpdatePinnedMessage(ctx, request)
	if err != nil {
		return fmt.Errorf("pin message: %w", err)
	}
	return nil
}

func (tb *telegramBackend) SearchMessages(ctx context.Context, ch chatEntry, query string) ([]message, error) {
	msgs, err := tb.c.searchMessages(ctx, entryToChat(ch), query)
	if err != nil {
		return nil, err
	}
	return tgMessagesToMessages(tb.c, msgs), nil
}

func (tb *telegramBackend) Subscribe(ch chatEntry) {
	tb.c.subscribe(entryToChat(ch).key)
}

func (tb *telegramBackend) Unsubscribe() {
	tb.c.subscribe(peerKey{})
}

func (tb *telegramBackend) Drain() ([]message, []genericReactionUpdate, []string) {
	msgs, reactions, deletions := tb.c.drain()
	genMsgs := tgMessagesToMessages(tb.c, msgs)
	genReactions := make([]genericReactionUpdate, len(reactions))
	for i, r := range reactions {
		genReactions[i] = genericReactionUpdate{
			MessageID: strconv.Itoa(r.messageID),
			Reactions: tgReactionsToMessageReactions(r.reactions),
		}
	}
	genDeletions := make([]string, len(deletions))
	for i, id := range deletions {
		genDeletions[i] = strconv.Itoa(id)
	}
	return genMsgs, genReactions, genDeletions
}

func (tb *telegramBackend) Changed() <-chan struct{} { return tb.c.changed }
func (tb *telegramBackend) IsTyping() bool           { return tb.c.isTyping() }
func (tb *telegramBackend) ClearTyping()             { tb.c.clearTyping() }

func (tb *telegramBackend) ReadMarker(ch chatEntry, msg message) string {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return ""
	}
	return tb.c.readMarker(entryToChat(ch), m)
}

func (tb *telegramBackend) RefreshReadState(ctx context.Context, ch chatEntry) error {
	return tb.c.refreshReadState(ctx, entryToChat(ch))
}

func (tb *telegramBackend) RefreshChatUnread(ctx context.Context, ch chatEntry) (int, error) {
	return tb.c.refreshChatUnread(ctx, entryToChat(ch))
}

func (tb *telegramBackend) CachedUnread(ch chatEntry) int {
	return tb.c.cachedChatUnread(entryToChat(ch).key)
}

func (tb *telegramBackend) InboxPosition(ch chatEntry) string {
	state := tb.c.readState(entryToChat(ch).key)
	return strconv.Itoa(state.inbox)
}

func (tb *telegramBackend) MediaPlayable(msg message) bool {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return false
	}
	_, err := mediaAttachment(m)
	return err == nil
}

func (tb *telegramBackend) PlayMedia(ctx context.Context, t *terminal, msg message, player string) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("this message has no playable attachment")
	}
	a, err := mediaAttachment(m)
	if err != nil {
		return err
	}
	return tb.c.playMedia(ctx, t, a, player)
}

func (tb *telegramBackend) MediaSaveable(msg message) (string, bool) {
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

func (tb *telegramBackend) SaveMedia(ctx context.Context, msg message, destPath string) error {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	dl, err := mediaDownloadable(m)
	if err != nil {
		return err
	}
	return tb.c.saveMedia(ctx, dl, destPath)
}

func (tb *telegramBackend) AcknowledgeMedia(ctx context.Context, msg message) {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return
	}
	tb.c.acknowledgeMedia(ctx, m)
}

func (tb *telegramBackend) SearchUsers(ctx context.Context, t *terminal, recentChats []chatEntry) (chatEntry, bool, error) {
	chats := chatEntriesToChats(recentChats)
	result, selected, err := tb.c.searchUsers(ctx, t, chats)
	if err != nil {
		return chatEntry{}, false, err
	}
	if !selected {
		return chatEntry{}, false, nil
	}
	return chatToChatEntry(result), true, nil
}

func (tb *telegramBackend) HandleForward(ctx context.Context, t *terminal, recentChats []chatEntry, fromChat chatEntry, msgID string) {
	chats := chatEntriesToChats(recentChats)
	fwd := forwardRequest{from: entryToChat(fromChat).peer, id: msgIDToInt(msgID)}
	tb.c.handleForward(ctx, t, chats, fwd)
}

func (tb *telegramBackend) AIAvailable() bool { return tb.c.aiAvailable() }

func (tb *telegramBackend) AIOn(ch chatEntry) bool {
	return tb.c.aiOn(entryToChat(ch))
}

func (tb *telegramBackend) AIToggle(ch chatEntry) (bool, error) {
	return tb.c.aiToggle(entryToChat(ch))
}

func (tb *telegramBackend) AIShouldAnswer(msg message) bool {
	m, ok := msg.Raw.(*tg.Message)
	if !ok {
		return false
	}
	return tb.c.aiShouldAnswer(m)
}

func (tb *telegramBackend) AIRequest(ch chatEntry, msgs []message) (aiPrompt, error) {
	tgMsgs := make([]*tg.Message, 0, len(msgs))
	for _, msg := range msgs {
		if m, ok := msg.Raw.(*tg.Message); ok {
			tgMsgs = append(tgMsgs, m)
		}
	}
	return tb.c.aiRequest(entryToChat(ch), tgMsgs)
}

func (tb *telegramBackend) AIComplete(ctx context.Context, prompt aiPrompt) (string, error) {
	if tb.c.ai == nil {
		return "", fmt.Errorf("AI is not configured")
	}
	return tb.c.ai.complete(ctx, prompt)
}

func (tb *telegramBackend) RecentEmoji() []string                   { return tb.c.recentEmoji() }
func (tb *telegramBackend) RememberEmoji(value string)              { tb.c.rememberEmoji(value) }
func (tb *telegramBackend) EmojiChoices(query string) []emojiOption { return tb.c.emojiChoices(query) }
func (tb *telegramBackend) ReactionChoices(query string) []emojiOption {
	return tb.c.reactionChoices(query)
}
