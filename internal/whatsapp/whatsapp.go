package whatsapp

import (
	"cmp"
	"context"
	"fmt"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"wiregram/internal/backend"
	"wiregram/internal/config"
	"wiregram/internal/textfmt"
)

type WaBackend struct {
	Client    *whatsmeow.Client
	container *sqlstore.Container
	Jid       types.JID
	PushName  string

	mu        sync.Mutex
	typing    map[types.JID]time.Time
	unreads   map[types.JID]int
	chatCache []waChatEntry
	msgCache  map[types.JID][]backend.Message
	userCache map[types.JID]string
	// members are each group's participants as last listed for a mention,
	// so Send can tell which @numbers in a message are mentions.
	members map[types.JID][]types.JID
	// reactions holds who reacted with what, by message ID then reacting
	// user, since WhatsApp sends reactions as separate messages.
	reactions   map[string]map[string]string
	recentEmoji []string
	events      backend.Bus
	// PairDeadline is told when the pairing socket will close.
	PairDeadline func(time.Time)
	// OnLoggedOut is told when the phone unlinks this device.
	OnLoggedOut func(error)
	// syncing holds from connecting until the chats first arrive: the history
	// sync after pairing, or the missed events after reconnecting.
	syncing atomic.Bool
	paired  atomic.Bool

	configDir string
}

// pairWindow is how long whatsmeow keeps the pairing socket open: it closes it
// once its six QR codes expire, the first after 60s and the rest after 20s.
const pairWindow = 160 * time.Second

// syncWait is how long the chat list shows as loading at most.
const syncWait = 2 * time.Minute

type waChatEntry struct {
	jid     types.JID
	name    string
	lastAt  time.Time
	unread  int
	isGroup bool
}

func NewWABackend(configDir string) *WaBackend {
	return &WaBackend{
		typing:    make(map[types.JID]time.Time),
		unreads:   make(map[types.JID]int),
		chatCache: nil,
		msgCache:  make(map[types.JID][]backend.Message),
		userCache: make(map[types.JID]string),
		members:   make(map[types.JID][]types.JID),
		reactions: make(map[string]map[string]string),
		configDir: configDir,
	}
}

func (wa *WaBackend) Name() string { return "whatsapp" }

func (wa *WaBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		Forward:       true,
		OutboxReceipt: true,
		ViewOnce:      true,
		Reactions:     true,
		Edit:          true,
		Pin:           false,
		Upload:        true,
		Search:        false,
		Typing:        true,
		DeleteChat:    false,
		Archive:       false,
	}
}

func (wa *WaBackend) openStore(ctx context.Context) error {
	dbPath := filepath.Join(wa.configDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite", "file:"+dbPath+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", nil)
	if err != nil {
		return fmt.Errorf("open WhatsApp session store: %w", err)
	}
	wa.container = container
	return nil
}

func (wa *WaBackend) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	if wa.container == nil {
		if err := wa.openStore(ctx); err != nil {
			return err
		}
	}
	deviceStore, err := wa.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load WhatsApp device: %w", err)
	}
	wa.Client = whatsmeow.NewClient(deviceStore, nil)
	wa.Client.AddEventHandler(wa.handleEvent)

	if wa.Client.Store.ID != nil {
		wa.startSync()
		if err := wa.Client.Connect(); err != nil {
			return fmt.Errorf("reconnect to WhatsApp: %w", err)
		}
		wa.Jid = *wa.Client.Store.ID
		wa.PushName = wa.Client.Store.PushName
		return nil
	}

	if prompt == nil {
		return fmt.Errorf("WhatsApp account needs pairing")
	}

	qrChan, err := wa.Client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := wa.Client.Connect(); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}

	// Wait for QR event before requesting pair code.
	select {
	case <-ctx.Done():
		wa.Client.Disconnect()
		return ctx.Err()
	case item := <-qrChan:
		if item.Event != "code" {
			wa.Client.Disconnect()
			return fmt.Errorf("unexpected pairing event: %s", item.Event)
		}
	}
	if wa.PairDeadline != nil {
		wa.PairDeadline(time.Now().Add(pairWindow))
	}

	phone, err := prompt("Phone number (international, e.g. +91...): ", false)
	if err != nil {
		wa.Client.Disconnect()
		return err
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		wa.Client.Disconnect()
		return fmt.Errorf("phone number is required")
	}

	code, err := wa.Client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		wa.Client.Disconnect()
		return fmt.Errorf("request pairing code: %w", err)
	}
	if len(code) > 4 {
		code = code[:4] + "-" + code[4:]
	}
	report(fmt.Errorf("Enter this code on your phone: %s", code))

	for item := range qrChan {
		switch item.Event {
		case "success":
			wa.paired.Store(true)
			wa.startSync()
			wa.Jid = *wa.Client.Store.ID
			wa.PushName = wa.Client.Store.PushName
			return nil
		case "timeout":
			wa.Client.Disconnect()
			return fmt.Errorf("pairing timed out; try again")
		case "error":
			wa.Client.Disconnect()
			return fmt.Errorf("pairing failed: %w", item.Error)
		case "code":
		default:
			wa.Client.Disconnect()
			return fmt.Errorf("pairing failed: %s", item.Event)
		}
	}
	wa.Client.Disconnect()
	return fmt.Errorf("pairing failed")
}

func (wa *WaBackend) Identity() backend.AccountIdentity {
	return backend.AccountIdentity{
		Name:  wa.PushName,
		Phone: wa.Jid.User,
	}
}

func (wa *WaBackend) AccountLabel() string {
	if wa.Jid.IsEmpty() {
		return "[WA] awaiting sign-in"
	}
	phone := wa.Jid.User
	if wa.PushName != "" {
		return fmt.Sprintf("[WA] %s (+%s)", textfmt.Plain(wa.PushName), phone)
	}
	return fmt.Sprintf("[WA] +%s", phone)
}

func (wa *WaBackend) Logout(ctx context.Context) error {
	if err := wa.Client.Logout(ctx); err != nil {
		return fmt.Errorf("unlink WhatsApp device: %w", err)
	}
	return nil
}

func (wa *WaBackend) Syncing() bool { return wa.syncing.Load() }

func (wa *WaBackend) startSync() {
	wa.syncing.Store(true)
	// The phone may never send a history sync; stop waiting for it.
	time.AfterFunc(syncWait, wa.finishSync)
}

func (wa *WaBackend) finishSync() {
	if wa.syncing.Swap(false) {
		wa.events.Publish(backend.Event{Kind: backend.EventUnread})
	}
}

func (wa *WaBackend) Run(ctx context.Context) error {
	<-ctx.Done()
	if wa.Client != nil {
		wa.Client.Disconnect()
	}
	return ctx.Err()
}

// Event handler.

func (wa *WaBackend) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		wa.handleMessage(evt)
	case *events.Receipt:
		wa.handleReceipt(evt)
	case *events.ChatPresence:
		wa.handlePresence(evt)
	case *events.HistorySync:
		wa.handleHistorySync(evt)
		if len(evt.Data.GetConversations()) > 0 {
			wa.syncing.Store(false)
			wa.events.Publish(backend.Event{Kind: backend.EventUnread})
		}
	case *events.OfflineSyncCompleted:
		if !wa.paired.Load() {
			wa.finishSync()
		}
	case *events.Connected:
		_ = wa.Client.SendPresence(context.Background(), types.PresenceAvailable)
	case *events.LoggedOut:
		wa.mu.Lock()
		wa.chatCache = nil
		wa.mu.Unlock()
		if wa.OnLoggedOut != nil {
			wa.OnLoggedOut(fmt.Errorf("this device was unlinked from WhatsApp; link it again"))
		}
	}
}

func (wa *WaBackend) handleMessage(evt *events.Message) {
	msg := wa.eventMessageToMessage(evt)

	chatJID := evt.Info.Chat
	if evt.Info.IsFromMe {
		if meta := evt.Info.DeviceSentMeta; meta != nil && meta.DestinationJID != "" {
			parsed, err := types.ParseJID(meta.DestinationJID)
			if err == nil {
				chatJID = parsed
			}
		}
	}

	if wa.isSelf(chatJID) {
		// Notes to self come under the own number or the own LID; keep one chat.
		chatJID = wa.Jid.ToNonAD()
	}

	if evt.Info.PushName != "" {
		wa.mu.Lock()
		wa.userCache[evt.Info.Sender.ToNonAD()] = evt.Info.PushName
		wa.mu.Unlock()
	}

	chat := backend.ChatID{Backend: "whatsapp", Id: chatJID.String()}

	// A revoke deletes the message for everyone; it is not a message itself.
	if pm := evt.Message.GetProtocolMessage(); pm != nil && !evt.IsEdit &&
		pm.GetType() == waE2E.ProtocolMessage_REVOKE {
		id := pm.GetKey().GetID()
		wa.mu.Lock()
		wa.forget(chatJID, id)
		wa.mu.Unlock()
		wa.events.Publish(backend.Event{Kind: backend.EventDelete, Chat: chat, Deleted: []string{id}})
		return
	}

	wa.mu.Lock()
	wa.touchChat(chatJID, evt.Info.Timestamp)
	reaction := evt.Message.GetReactionMessage()
	if !evt.IsEdit && reaction == nil && !evt.Info.IsFromMe {
		wa.unreads[chatJID]++
	}
	switch {
	case evt.IsEdit:
		wa.edited(chatJID, getEditTargetID(evt), msg)
	case reaction == nil:
		wa.remember(chatJID, msg)
	}
	var reactions []backend.MessageReaction
	if reaction != nil {
		user := evt.Info.Sender.User
		if evt.Info.IsFromMe {
			user = wa.Jid.User
		}
		target := reaction.GetKey().GetID()
		wa.setReaction(target, user, reaction.GetText())
		reactions = wa.reactionsOf(target)
	}
	// Person-to-person only: not groups, broadcasts or the own number.
	self := wa.isSelf(chatJID)
	dm := (chatJID.Server == types.DefaultUserServer || chatJID.Server == types.HiddenUserServer) &&
		!self
	wa.mu.Unlock()

	switch {
	case evt.IsEdit:
		msg.ID = getEditTargetID(evt)
		wa.events.Publish(backend.Event{Kind: backend.EventEdit, Chat: chat, DM: dm, Self: self,
			Message: &msg})
	case reaction != nil:
		wa.events.Publish(backend.Event{
			Kind: backend.EventReaction, Chat: chat,
			Reaction: &backend.GenericReactionUpdate{
				MessageID: reaction.GetKey().GetID(), Reactions: reactions,
			},
		})
	default:
		wa.events.Publish(backend.Event{Kind: backend.EventMessage, Chat: chat, DM: dm, Self: self,
			Message: &msg})
	}
}

func getEditTargetID(evt *events.Message) string {
	if evt.Message.GetEditedMessage() != nil {
		key := evt.Message.GetEditedMessage().GetMessage().GetProtocolMessage().GetKey()
		if key != nil {
			return key.GetID()
		}
	}
	if evt.RawMessage.GetEditedMessage() != nil {
		key := evt.RawMessage.GetEditedMessage().GetMessage().GetProtocolMessage().GetKey()
		if key != nil {
			return key.GetID()
		}
	}
	return evt.Info.ID
}

func (wa *WaBackend) handleReceipt(evt *events.Receipt) {
	if evt.Type != types.ReceiptTypeRead && evt.Type != types.ReceiptTypeReadSelf {
		return
	}
	if evt.Type == types.ReceiptTypeReadSelf {
		chatJID := evt.Chat
		wa.mu.Lock()
		wa.unreads[chatJID] = 0
		wa.mu.Unlock()
		wa.events.Publish(backend.Event{
			Kind: backend.EventUnread, Chat: backend.ChatID{Backend: "whatsapp", Id: chatJID.String()},
		})
	}
}

func (wa *WaBackend) handlePresence(evt *events.ChatPresence) {
	wa.mu.Lock()
	if evt.State == types.ChatPresenceComposing {
		wa.typing[evt.Sender] = time.Now()
	} else {
		delete(wa.typing, evt.Sender)
	}
	wa.mu.Unlock()
	wa.events.Publish(backend.Event{
		Kind: backend.EventTyping, Chat: backend.ChatID{Backend: "whatsapp", Id: evt.Chat.String()},
	})
}

func (wa *WaBackend) handleHistorySync(evt *events.HistorySync) {
	data := evt.Data
	if data == nil {
		return
	}
	wa.mu.Lock()
	defer wa.mu.Unlock()

	for _, conv := range data.GetConversations() {
		jidStr := conv.GetID()
		if jidStr == "" {
			continue
		}
		jid, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}
		name := conv.GetDisplayName()
		if name == "" {
			name = conv.GetName()
		}
		var lastAt time.Time
		if ts := conv.GetConversationTimestamp(); ts > 0 {
			lastAt = time.Unix(int64(ts), 0)
		}
		// One count per chat for Dialogs and CachedUnread alike; live
		// messages and read receipts update it from here on.
		wa.unreads[jid] = int(conv.GetUnreadCount())
		isGroup := jid.Server == types.GroupServer

		found := false
		for i := range wa.chatCache {
			if wa.chatCache[i].jid == jid {
				wa.chatCache[i].name = name
				if !lastAt.IsZero() {
					wa.chatCache[i].lastAt = lastAt
				}
				found = true
				break
			}
		}
		if !found {
			wa.chatCache = append(wa.chatCache, waChatEntry{
				jid:     jid,
				name:    name,
				lastAt:  lastAt,
				isGroup: isGroup,
			})
		}

		msgs := wa.historySyncMessages(jid, conv.GetMessages())
		if len(msgs) > 0 {
			existing := wa.msgCache[jid]
			seen := make(map[string]bool, len(existing))
			for _, m := range existing {
				seen[m.ID] = true
			}
			for _, m := range msgs {
				if !seen[m.ID] {
					existing = append(existing, m)
				}
			}
			sort.Slice(existing, func(i, j int) bool {
				return existing[i].Timestamp.Before(existing[j].Timestamp)
			})
			wa.msgCache[jid] = existing
		}
	}

	for _, pn := range data.GetPushnames() {
		if pn.GetID() != "" && pn.GetPushname() != "" {
			jid, err := types.ParseJID(pn.GetID())
			if err == nil {
				wa.userCache[jid.ToNonAD()] = pn.GetPushname()
			}
		}
	}
}

func (wa *WaBackend) historySyncMessages(chatJID types.JID, hsmsgs []*waHistorySync.HistorySyncMsg) []backend.Message {
	var result []backend.Message
	for _, hsm := range hsmsgs {
		wmi := hsm.GetMessage()
		if wmi == nil || wmi.GetMessage() == nil {
			continue
		}
		key := wmi.GetKey()
		if key == nil {
			continue
		}
		msg := wa.webMessageToMessage(chatJID, wmi)
		if msg.ID == "" {
			continue
		}
		for _, r := range wmi.GetReactions() {
			wa.setReaction(msg.ID, wa.reactionUser(r.GetKey()), r.GetText())
		}
		result = append(result, msg)
	}
	return result
}

// msgCacheLimit is how many messages a chat keeps in memory.
const msgCacheLimit = 500

// remember keeps msg among chatJID's messages, for History, Mentions,
// Forward and finding a message's sender. The caller holds wa.mu.
func (wa *WaBackend) remember(chatJID types.JID, msg backend.Message) {
	cached := wa.msgCache[chatJID]
	for _, m := range cached {
		if m.ID == msg.ID {
			return
		}
	}
	cached = append(cached, msg)
	if len(cached) > msgCacheLimit {
		cached = slices.Clone(cached[len(cached)-msgCacheLimit:])
	}
	wa.msgCache[chatJID] = cached
}

// edited applies an edit to the cached message id. The caller holds wa.mu.
func (wa *WaBackend) edited(chatJID types.JID, id string, msg backend.Message) {
	cached := wa.msgCache[chatJID]
	for i := range cached {
		if cached[i].ID == id {
			cached[i].Text, cached[i].Spans, cached[i].Source = msg.Text, msg.Spans, msg.Source
			return
		}
	}
}

// forget drops the cached message id and its reactions. The caller holds wa.mu.
func (wa *WaBackend) forget(chatJID types.JID, id string) {
	wa.msgCache[chatJID] = slices.DeleteFunc(wa.msgCache[chatJID],
		func(m backend.Message) bool { return m.ID == id })
	delete(wa.reactions, id)
}

// senderOf is who sent the cached message id in chatJID, or the empty JID
// when it is not cached.
func (wa *WaBackend) senderOf(chatJID types.JID, id string) types.JID {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	for _, m := range wa.msgCache[chatJID] {
		if m.ID == id && m.SenderID != "" {
			if jid, err := types.ParseJID(m.SenderID); err == nil {
				return jid
			}
		}
	}
	return types.EmptyJID
}

// quotedParticipant is who wrote the message replyTo in jid, for the
// reply's context; unknown in a group, it is left out.
func (wa *WaBackend) quotedParticipant(jid types.JID, replyTo string) *string {
	sender := wa.senderOf(jid, replyTo)
	if sender.IsEmpty() {
		if jid.Server == types.GroupServer {
			return nil
		}
		sender = jid // in a DM, most replies quote the other person
	}
	return proto.String(sender.ToNonAD().String())
}

// sent caches a message this device sent, which WhatsApp does not echo.
func (wa *WaBackend) sent(jid types.JID, msg backend.Message) {
	msg.SenderID = wa.Jid.ToNonAD().String()
	wa.mu.Lock()
	wa.touchChat(jid, msg.Timestamp)
	wa.remember(jid, msg)
	wa.mu.Unlock()
}

func (wa *WaBackend) touchChat(chatJID types.JID, ts time.Time) {
	for i := range wa.chatCache {
		if wa.chatCache[i].jid == chatJID {
			if ts.After(wa.chatCache[i].lastAt) {
				wa.chatCache[i].lastAt = ts
			}
			return
		}
	}
	name := wa.contactName(chatJID)
	wa.chatCache = append(wa.chatCache, waChatEntry{
		jid:     chatJID,
		name:    name,
		lastAt:  ts,
		isGroup: chatJID.Server == types.GroupServer,
	})
}

// contactName names jid from the push names seen so far. The caller holds wa.mu.
func (wa *WaBackend) contactName(jid types.JID) string {
	nonAD := jid.ToNonAD()
	if name, ok := wa.userCache[nonAD]; ok && name != "" {
		return name
	}
	if jid.Server == types.GroupServer {
		return jid.User
	}
	return "+" + jid.User
}

// lookupName is contactName for callers that do not hold wa.mu.
func (wa *WaBackend) lookupName(jid types.JID) string {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.contactName(jid)
}

// Chat list.

func (wa *WaBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}
	wa.mu.Lock()
	cache := make([]waChatEntry, len(wa.chatCache))
	copy(cache, wa.chatCache)
	wa.mu.Unlock()

	sort.Slice(cache, func(i, j int) bool {
		return cache[i].lastAt.After(cache[j].lastAt)
	})
	if len(cache) > 20 {
		cache = cache[:20]
	}

	entries := make([]backend.ChatEntry, 0, len(cache))
	for _, c := range cache {
		entries = append(entries, wa.chatEntry(c))
	}
	return entries, 0, nil
}

func (wa *WaBackend) chatEntry(c waChatEntry) backend.ChatEntry {
	wa.mu.Lock()
	title := c.name
	if title == "" {
		title = wa.contactName(c.jid)
	}
	unread := wa.unreads[c.jid]
	wa.mu.Unlock()
	return backend.ChatEntry{
		ID:      backend.ChatID{Backend: "whatsapp", Id: c.jid.String()},
		Title:   title,
		Unread:  unread,
		LastAt:  c.lastAt,
		IsDM:    c.jid.Server == types.DefaultUserServer,
		IsGroup: c.isGroup,
		IsSelf:  wa.isSelf(c.jid),
		Raw:     c.jid,
	}
}

// Mentions scans the messages held this session: WhatsApp has no server
// search.
func (wa *WaBackend) Mentions(ctx context.Context, limit int) ([]backend.Mention, error) {
	wa.mu.Lock()
	cache := make([]waChatEntry, len(wa.chatCache))
	copy(cache, wa.chatCache)
	msgs := make(map[types.JID][]backend.Message, len(cache))
	for _, c := range cache {
		if c.isGroup {
			msgs[c.jid] = wa.msgCache[c.jid]
		}
	}
	wa.mu.Unlock()
	var found []backend.Mention
	for _, c := range cache {
		if !c.isGroup {
			continue
		}
		entry := wa.chatEntry(c)
		for _, msg := range msgs[c.jid] {
			if msg.Mentioned {
				found = append(found, backend.Mention{Chat: entry, Message: msg})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].Message.Timestamp.After(found[j].Message.Timestamp)
	})
	return found[:min(len(found), limit)], nil
}

// isSelf reports whether jid is the user's own chat, by phone number or LID.
func (wa *WaBackend) isSelf(jid types.JID) bool {
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return false
	}
	lid := ""
	if wa.Client != nil && wa.Client.Store != nil {
		lid = wa.Client.Store.LID.User
	}
	return jid.User == wa.Jid.User || lid != "" && jid.User == lid
}

// mentionsMe reports whether m mentions the user, by phone number or by the
// LID newer groups mention with.
func (wa *WaBackend) mentionsMe(m *waE2E.Message) bool {
	lid := ""
	if wa.Client != nil && wa.Client.Store != nil {
		lid = wa.Client.Store.LID.User
	}
	for _, s := range replyContext(m).GetMentionedJID() {
		jid, err := types.ParseJID(s)
		if err != nil {
			continue
		}
		if jid.User == wa.Jid.User || lid != "" && jid.User == lid {
			return true
		}
	}
	return false
}

// Message history.

func (wa *WaBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return nil, err
	}
	wa.mu.Lock()
	defer wa.mu.Unlock()
	cached := wa.msgCache[jid]
	if len(cached) > 50 {
		cached = cached[len(cached)-50:]
	}
	result := make([]backend.Message, len(cached))
	copy(result, cached)
	for i := range result {
		result[i].Reactions = wa.reactionsOf(result[i].ID)
	}
	return result, nil
}

func (wa *WaBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	return nil, nil
}

// Sending.

func (wa *WaBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return backend.Message{}, err
	}
	msg := &waE2E.Message{}
	mentioned := wa.mentionedMembers(jid, text)
	if replyTo != "" || len(mentioned) > 0 {
		info := &waE2E.ContextInfo{MentionedJID: mentioned}
		if replyTo != "" {
			info.StanzaID = proto.String(replyTo)
			info.Participant = wa.quotedParticipant(jid, replyTo)
			info.QuotedMessage = &waE2E.Message{Conversation: proto.String("")}
		}
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: info,
		}
	} else {
		msg.Conversation = proto.String(text)
	}
	resp, err := wa.Client.SendMessage(ctx, jid, msg)
	if err != nil {
		return backend.Message{}, fmt.Errorf("send message: %w", err)
	}
	out := backend.Message{
		ID:        resp.ID,
		Sender:    "you",
		Text:      text,
		Timestamp: resp.Timestamp,
		Outgoing:  true,
		ReplyToID: replyTo,
	}
	wa.sent(jid, out)
	return out, nil
}

func (wa *WaBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, filePath, caption, replyTo string) (backend.Message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return backend.Message{}, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return backend.Message{}, fmt.Errorf("read file: %w", err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var mediaType whatsmeow.MediaType
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = whatsmeow.MediaImage
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = whatsmeow.MediaVideo
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = whatsmeow.MediaAudio
	default:
		mediaType = whatsmeow.MediaDocument
	}
	uploaded, err := wa.Client.Upload(ctx, data, mediaType)
	if err != nil {
		return backend.Message{}, fmt.Errorf("upload file: %w", err)
	}
	var waMsg *waE2E.Message
	var ctxInfo *waE2E.ContextInfo
	if replyTo != "" {
		ctxInfo = &waE2E.ContextInfo{
			StanzaID:      proto.String(replyTo),
			Participant:   wa.quotedParticipant(jid, replyTo),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("")},
		}
	}
	fileName := filepath.Base(filePath)
	switch mediaType {
	case whatsmeow.MediaImage:
		waMsg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           &uploaded.URL,
			DirectPath:    &uploaded.DirectPath,
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    &uploaded.FileLength,
			ContextInfo:   ctxInfo,
		}}
	case whatsmeow.MediaVideo:
		waMsg = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           &uploaded.URL,
			DirectPath:    &uploaded.DirectPath,
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    &uploaded.FileLength,
			ContextInfo:   ctxInfo,
		}}
	case whatsmeow.MediaAudio:
		waMsg = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype:      proto.String(mimeType),
			URL:           &uploaded.URL,
			DirectPath:    &uploaded.DirectPath,
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    &uploaded.FileLength,
			ContextInfo:   ctxInfo,
		}}
	default:
		waMsg = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Title:         proto.String(caption),
			FileName:      proto.String(fileName),
			Mimetype:      proto.String(mimeType),
			URL:           &uploaded.URL,
			DirectPath:    &uploaded.DirectPath,
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    &uploaded.FileLength,
			ContextInfo:   ctxInfo,
		}}
	}
	resp, err := wa.Client.SendMessage(ctx, jid, waMsg)
	if err != nil {
		return backend.Message{}, fmt.Errorf("send media: %w", err)
	}
	label := mediaLabelForMIME(mimeType, fileName, int64(uploaded.FileLength))
	out := backend.Message{
		ID:         resp.ID,
		Sender:     "you",
		Text:       caption,
		MediaLabel: label,
		Timestamp:  resp.Timestamp,
		Outgoing:   true,
		ReplyToID:  replyTo,
	}
	wa.sent(jid, out)
	return out, nil
}

func mediaLabelForMIME(mimeType, fileName string, size int64) string {
	kind := "file"
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		kind = "photo"
	case strings.HasPrefix(mimeType, "video/"):
		kind = "video"
	case strings.HasPrefix(mimeType, "audio/"):
		kind = "audio"
	}
	sizeStr := textfmt.FormatSize(size)
	return fmt.Sprintf("[%s] %s, %s", kind, fileName, sizeStr)
}

func (wa *WaBackend) UploadPlaceholder(path, caption string) backend.Message {
	label := "[uploading] " + filepath.Base(path)
	return backend.Message{
		ID:         "upload-placeholder",
		Sender:     "you",
		Text:       caption,
		MediaLabel: label,
		Timestamp:  time.Now(),
		Outgoing:   true,
	}
}

func (wa *WaBackend) Editable(msg backend.Message) error {
	return backend.GenericEditable(msg)
}

func (wa *WaBackend) Edit(ctx context.Context, ch backend.ChatEntry, msg backend.Message, text string) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	editMsg := &waE2E.Message{
		EditedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Key: &waCommon.MessageKey{
						RemoteJID: proto.String(jid.String()),
						FromMe:    proto.Bool(true),
						ID:        proto.String(msg.ID),
					},
					Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
					EditedMessage: &waE2E.Message{
						Conversation: proto.String(text),
					},
				},
			},
		},
	}
	_, err = wa.Client.SendMessage(ctx, jid, editMsg)
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	return nil
}

func (wa *WaBackend) DeleteMessages(ctx context.Context, ch backend.ChatEntry, ids []string, forEveryone bool) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	// Deleting only for yourself is an app-state patch whatsmeow cannot build;
	// a revoke would delete the message for everyone.
	if !forEveryone {
		return fmt.Errorf("WhatsApp can only delete messages for everyone")
	}
	for _, id := range ids {
		if _, err := wa.Client.RevokeMessage(ctx, jid, id); err != nil {
			return fmt.Errorf("revoke message: %w", err)
		}
		wa.mu.Lock()
		wa.forget(jid, id)
		wa.mu.Unlock()
	}
	return nil
}

func (wa *WaBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported for WhatsApp")
}

// React toggles the user's emoji on msg. WhatsApp allows one reaction per
// person, so another emoji replaces it and an empty one removes it.
func (wa *WaBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return nil, err
	}
	wa.mu.Lock()
	if wa.reactions[msg.ID][wa.Jid.User] == emoji {
		emoji = ""
	}
	wa.mu.Unlock()
	// The key names the message's sender, which a group reaction needs.
	sender := types.EmptyJID
	if parsed, err := types.ParseJID(msg.SenderID); err == nil && !msg.Outgoing {
		sender = parsed
	} else if !msg.Outgoing {
		sender = wa.senderOf(jid, msg.ID)
		if sender.IsEmpty() && jid.Server != types.GroupServer {
			sender = jid
		}
	}
	reactionMsg := wa.Client.BuildReaction(jid, sender, msg.ID, emoji)
	if _, err := wa.Client.SendMessage(ctx, jid, reactionMsg); err != nil {
		return nil, fmt.Errorf("send reaction: %w", err)
	}
	wa.mu.Lock()
	defer wa.mu.Unlock()
	wa.setReaction(msg.ID, wa.Jid.User, emoji)
	return wa.reactionsOf(msg.ID), nil
}

// setReaction records user's reaction to msgID; an empty emoji removes it.
// The caller holds wa.mu.
func (wa *WaBackend) setReaction(msgID, user, emoji string) {
	if emoji == "" {
		delete(wa.reactions[msgID], user)
		return
	}
	if wa.reactions[msgID] == nil {
		wa.reactions[msgID] = make(map[string]string)
	}
	wa.reactions[msgID][user] = emoji
}

// reactionsOf counts msgID's reactions, most used first. The caller holds wa.mu.
func (wa *WaBackend) reactionsOf(msgID string) []backend.MessageReaction {
	var result []backend.MessageReaction
	index := make(map[string]int)
	for user, emoji := range wa.reactions[msgID] {
		i, ok := index[emoji]
		if !ok {
			i = len(result)
			index[emoji] = i
			result = append(result, backend.MessageReaction{Emoji: emoji})
		}
		result[i].Count++
		if user == wa.Jid.User {
			result[i].Chosen = true
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Emoji < result[j].Emoji
	})
	return result
}

// reactionUser is the user who sent the reaction with key.
func (wa *WaBackend) reactionUser(key *waCommon.MessageKey) string {
	if key.GetFromMe() {
		return wa.Jid.User
	}
	sender := key.GetParticipant()
	if sender == "" {
		sender = key.GetRemoteJID()
	}
	jid, err := types.ParseJID(sender)
	if err != nil {
		return sender
	}
	return jid.User
}

func (wa *WaBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	// WhatsApp needs the message's sender in groups.
	err = wa.Client.MarkRead(ctx, []types.MessageID{upToID}, time.Now(), jid,
		wa.senderOf(jid, upToID))
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	wa.mu.Lock()
	wa.unreads[jid] = 0
	wa.mu.Unlock()
	return nil
}

func (wa *WaBackend) SetTyping(ctx context.Context, ch backend.ChatEntry) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return
	}
	_ = wa.Client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText)
}

func (wa *WaBackend) Forward(ctx context.Context, from backend.ChatEntry, to backend.ChatEntry, msgID string) error {
	toJID, err := wa.chatJID(to)
	if err != nil {
		return err
	}
	var original *backend.Message
	fromJID, err := wa.chatJID(from)
	if err != nil {
		return err
	}
	wa.mu.Lock()
	for i := range wa.msgCache[fromJID] {
		if wa.msgCache[fromJID][i].ID == msgID {
			m := wa.msgCache[fromJID][i]
			original = &m
			break
		}
	}
	wa.mu.Unlock()
	if original == nil {
		return fmt.Errorf("message not found in cache")
	}
	text := original.Text
	if text == "" {
		text = original.MediaLabel
	}
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				IsForwarded:     proto.Bool(true),
				ForwardingScore: proto.Uint32(1),
			},
		},
	}
	_, err = wa.Client.SendMessage(ctx, toJID, msg)
	if err != nil {
		return fmt.Errorf("forward message: %w", err)
	}
	return nil
}

func (wa *WaBackend) Pin(ctx context.Context, ch backend.ChatEntry, msg backend.Message, unpin bool) error {
	return fmt.Errorf("pinning is not supported for WhatsApp")
}

func (wa *WaBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
	return nil, fmt.Errorf("search is not available for WhatsApp")
}

// Real-time events.

// SetActive is a no-op: WhatsApp keeps no per-open-chat state.
func (wa *WaBackend) SetActive(backend.ChatEntry) {}

func (wa *WaBackend) Events() *backend.Bus { return &wa.events }

func (wa *WaBackend) IsTyping() bool {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	cutoff := time.Now().Add(-6 * time.Second)
	for _, t := range wa.typing {
		if t.After(cutoff) {
			return true
		}
	}
	return false
}

func (wa *WaBackend) ClearTyping() {
	wa.mu.Lock()
	wa.typing = make(map[types.JID]time.Time)
	wa.mu.Unlock()
}

// Read state.

func (wa *WaBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string      { return "" }
func (wa *WaBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error { return nil }
func (wa *WaBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	jid, err := wa.chatJID(ch)
	if err != nil {
		return 0, err
	}
	return wa.unreads[jid], nil
}

func (wa *WaBackend) CachedUnread(ch backend.ChatEntry) int {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	jid, _ := wa.chatJID(ch)
	return wa.unreads[jid]
}

func (wa *WaBackend) InboxPosition(ch backend.ChatEntry) string { return "" }

// Media.

func (wa *WaBackend) MediaPlayable(msg backend.Message) bool {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return false
	}
	m := evt.Message
	return m.GetImageMessage() != nil || m.GetVideoMessage() != nil ||
		m.GetAudioMessage() != nil || m.GetStickerMessage() != nil
}

func (wa *WaBackend) MediaExec(ctx context.Context, msg backend.Message, player string) (*exec.Cmd, func(), error) {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return nil, nil, fmt.Errorf("this message has no playable media")
	}
	var dl whatsmeow.DownloadableMessage
	var filename string
	var isAudio bool
	m := evt.Message
	switch {
	case m.GetImageMessage() != nil:
		dl, filename = m.GetImageMessage(), "image.jpg"
	case m.GetVideoMessage() != nil:
		dl, filename = m.GetVideoMessage(), "video.mp4"
	case m.GetAudioMessage() != nil:
		dl, filename, isAudio = m.GetAudioMessage(), "audio.ogg", true
	case m.GetStickerMessage() != nil:
		dl, filename = m.GetStickerMessage(), "sticker.webp"
	default:
		return nil, nil, fmt.Errorf("this message has no playable media")
	}
	data, err := wa.Client.Download(ctx, dl)
	if err != nil {
		return nil, nil, fmt.Errorf("download media: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "wiregram-wa-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }
	dest := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(dest, data, 0600); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write media file: %w", err)
	}
	return backend.MpvCmd(ctx, player, isAudio, dest), cleanup, nil
}

func (wa *WaBackend) MediaSaveable(msg backend.Message) (string, bool) {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return "", false
	}
	m := evt.Message
	switch {
	case m.GetImageMessage() != nil:
		return "image.jpg", true
	case m.GetVideoMessage() != nil:
		return "video.mp4", true
	case m.GetAudioMessage() != nil:
		return "audio.ogg", true
	case m.GetDocumentMessage() != nil:
		return backend.SafeFilename(m.GetDocumentMessage().GetFileName(), "document"), true
	case m.GetStickerMessage() != nil:
		return "sticker.webp", true
	}
	return "", false
}

func (wa *WaBackend) SaveMedia(ctx context.Context, msg backend.Message, destDir string) error {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return fmt.Errorf("this message has no downloadable attachment")
	}
	var dl whatsmeow.DownloadableMessage
	var filename string
	m := evt.Message
	switch {
	case m.GetImageMessage() != nil:
		dl = m.GetImageMessage()
		filename = "image.jpg"
	case m.GetVideoMessage() != nil:
		dl = m.GetVideoMessage()
		filename = "video.mp4"
	case m.GetAudioMessage() != nil:
		dl = m.GetAudioMessage()
		filename = "audio.ogg"
	case m.GetDocumentMessage() != nil:
		dl = m.GetDocumentMessage()
		filename = backend.SafeFilename(m.GetDocumentMessage().GetFileName(), "document")
	case m.GetStickerMessage() != nil:
		dl = m.GetStickerMessage()
		filename = "sticker.webp"
	default:
		return fmt.Errorf("this message has no downloadable attachment")
	}
	data, err := wa.Client.Download(ctx, dl)
	if err != nil {
		return fmt.Errorf("download media: %w", err)
	}
	if err := backend.WriteUnique(destDir, filename, data); err != nil {
		return fmt.Errorf("save media: %w", err)
	}
	return nil
}

func (wa *WaBackend) AcknowledgeMedia(ctx context.Context, msg backend.Message) {}

// User search.

func (wa *WaBackend) UserSearchBase(ctx context.Context, recentChats []backend.ChatEntry) (backend.UserSearchBase, error) {
	var base []backend.UserCandidate
	for _, ch := range recentChats {
		jid, ok := ch.Raw.(types.JID)
		if !ch.IsDM || !ok {
			continue
		}
		base = append(base, backend.UserCandidate{Key: jid.String(), Label: ch.Title,
			Match: strings.ToLower(ch.Title + " " + jid.User), Raw: ch})
	}
	return backend.UserSearchBase{Heading: "Recent DMs", Base: base, Local: base}, nil
}

// UserSearchRemote returns nothing: WhatsApp has no user directory to search.
func (wa *WaBackend) UserSearchRemote(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	return nil, nil
}

// CanMention is true for groups.
func (wa *WaBackend) CanMention(ch backend.ChatEntry) bool {
	jid, err := wa.chatJID(ch)
	return err == nil && jid.Server == types.GroupServer
}

// MentionBase lists the group's participants. A mention is written as
// @number, which Send turns into a mentioned JID.
func (wa *WaBackend) MentionBase(ctx context.Context, ch backend.ChatEntry) (backend.UserSearchBase, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return backend.UserSearchBase{}, err
	}
	info, err := wa.Client.GetGroupInfo(ctx, jid)
	if err != nil {
		return backend.UserSearchBase{}, fmt.Errorf("load group members: %w", err)
	}
	var out []backend.UserCandidate
	var members []types.JID
	wa.mu.Lock()
	for _, p := range info.Participants {
		members = append(members, p.JID)
		phone := p.PhoneNumber
		if phone.IsEmpty() && p.JID.Server == types.DefaultUserServer {
			phone = p.JID
		}
		if phone.User == wa.Jid.User || p.JID.User == wa.Jid.User {
			continue
		}
		name := p.DisplayName
		if !phone.IsEmpty() {
			name = wa.contactName(phone)
		}
		if name == "" {
			name = "+" + p.JID.User
		}
		label := name
		if !phone.IsEmpty() && name != "+"+phone.User {
			label += "  +" + phone.User
		}
		out = append(out, backend.UserCandidate{Key: p.JID.String(), Label: label,
			Match: strings.ToLower(name + " " + phone.User), Mention: "@" + p.JID.User + " ", Raw: p.JID})
	}
	wa.members[jid] = members
	wa.mu.Unlock()
	return backend.UserSearchBase{Heading: "Group members", Base: out, Local: out}, nil
}

func (wa *WaBackend) MentionSearch(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.UserCandidate, error) {
	return nil, nil
}

// mentionedMembers returns the participants of group jid written as @number in text.
func (wa *WaBackend) mentionedMembers(jid types.JID, text string) []string {
	if jid.Server != types.GroupServer || !strings.Contains(text, "@") {
		return nil
	}
	wa.mu.Lock()
	defer wa.mu.Unlock()
	var out []string
	for _, member := range wa.members[jid] {
		if containsToken(text, "@"+member.User) {
			out = append(out, member.String())
		}
	}
	return out
}

// containsToken reports whether text has token not followed by another digit,
// so @123 is not found inside @1234.
func containsToken(text, token string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], token)
		if j < 0 {
			return false
		}
		end := i + j + len(token)
		if end == len(text) || text[end] < '0' || text[end] > '9' {
			return true
		}
		i = end
	}
}

func (wa *WaBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	entry, ok := c.Raw.(backend.ChatEntry)
	if !ok {
		return backend.ChatEntry{}, fmt.Errorf("open chat: unexpected search result")
	}
	return entry, nil
}

// SelfChat is the chat with the user's own number, WhatsApp's "Message yourself".
func (wa *WaBackend) SelfChat(ctx context.Context) (backend.ChatEntry, error) {
	if wa.Jid.IsEmpty() {
		return backend.ChatEntry{}, fmt.Errorf("open your own chat: not signed in")
	}
	entry := wa.chatEntry(waChatEntry{jid: wa.Jid.ToNonAD()})
	entry.Title, entry.IsSelf = "You", true
	return entry, nil
}

// Emoji.

func (wa *WaBackend) RecentEmoji() []string { return wa.recentEmoji }

func (wa *WaBackend) RememberEmoji(value string) {
	for i, v := range wa.recentEmoji {
		if v == value {
			wa.recentEmoji = append(wa.recentEmoji[:i], wa.recentEmoji[i+1:]...)
			break
		}
	}
	wa.recentEmoji = append([]string{value}, wa.recentEmoji...)
	if len(wa.recentEmoji) > 20 {
		wa.recentEmoji = wa.recentEmoji[:20]
	}
}

func (wa *WaBackend) EmojiChoices(query string) []backend.EmojiOption {
	catalog := backend.EmojiCatalog()
	if query == "" {
		return backend.RecentFirst(catalog, wa.recentEmoji)
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

func (wa *WaBackend) ReactionChoices(query string) []backend.EmojiOption {
	return wa.EmojiChoices(query)
}

// Helpers.

func (wa *WaBackend) chatJID(ch backend.ChatEntry) (types.JID, error) {
	if jid, ok := ch.Raw.(types.JID); ok {
		return jid, nil
	}
	jid, err := types.ParseJID(ch.ID.Id)
	if err != nil {
		return types.EmptyJID, fmt.Errorf("parse chat JID: %w", err)
	}
	return jid, nil
}

func (wa *WaBackend) eventMessageToMessage(evt *events.Message) backend.Message {
	info := evt.Info
	senderName := info.PushName
	if senderName == "" {
		wa.mu.Lock()
		senderName = wa.userCache[info.Sender.ToNonAD()]
		wa.mu.Unlock()
	}
	if senderName == "" {
		senderName = "+" + info.Sender.User
	}
	outgoing := info.IsFromMe
	if outgoing {
		senderName = "you"
	}
	source, mediaLabel, multiline := wa.extractMessageContent(evt.Message)
	text, spans := backend.WhatsApp(source)
	return backend.Message{
		ID:         info.ID,
		Sender:     senderName,
		SenderID:   info.Sender.String(),
		Text:       text,
		Timestamp:  info.Timestamp,
		Outgoing:   outgoing,
		MediaLabel: mediaLabel,
		Image:      waInlineImage(evt.Message),
		Multiline:  multiline,
		ReplyToID:  extractReplyID(evt.Message),
		Quote:      wa.quoteOf(evt.Message, wa.lookupName),
		Forwarded:  isForwarded(evt.Message),
		Preview:    waPreview(evt.Message),
		Spans:      spans,
		Source:     source,
		Mentioned:  !outgoing && wa.mentionsMe(evt.Message),
		Raw:        evt,
	}
}

func (wa *WaBackend) webMessageToMessage(chatJID types.JID, wmi *waWeb.WebMessageInfo) backend.Message {
	key := wmi.GetKey()
	m := wmi.GetMessage()
	ts := time.Unix(int64(wmi.GetMessageTimestamp()), 0)
	outgoing := key.GetFromMe()
	senderName := wmi.GetPushName()
	if senderName == "" && !outgoing {
		participant := wmi.GetParticipant()
		if participant != "" {
			jid, err := types.ParseJID(participant)
			if err == nil {
				senderName = wa.contactName(jid)
			}
		}
		if senderName == "" {
			remoteJID := key.GetRemoteJID()
			if remoteJID != "" {
				jid, err := types.ParseJID(remoteJID)
				if err == nil {
					senderName = wa.contactName(jid)
				}
			}
		}
	}
	if outgoing {
		senderName = "you"
	}
	source, mediaLabel, multiline := wa.extractMessageContent(m)
	text, spans := backend.WhatsApp(source)
	// As a live message, so its media opens and its sender is known.
	var raw any
	senderID := ""
	if evt, err := wa.Client.ParseWebMessage(chatJID, wmi); err == nil {
		raw, senderID = evt, evt.Info.Sender.ToNonAD().String()
	}
	return backend.Message{
		ID:         key.GetID(),
		Sender:     senderName,
		SenderID:   senderID,
		Image:      raw != nil && waInlineImage(m),
		Raw:        raw,
		Text:       text,
		Timestamp:  ts,
		Outgoing:   outgoing,
		MediaLabel: mediaLabel,
		Multiline:  multiline,
		ReplyToID:  extractReplyID(m),
		Quote:      wa.quoteOf(m, wa.contactName),
		Forwarded:  isForwarded(m),
		Preview:    waPreview(m),
		Spans:      spans,
		Source:     source,
		Mentioned:  !outgoing && wa.mentionsMe(m),
	}
}

// waPreview returns the link preview the sender's phone attached, if any.
func waPreview(m *waE2E.Message) *backend.LinkPreview {
	ext := m.GetExtendedTextMessage()
	if ext == nil || ext.GetTitle() == "" {
		return nil
	}
	link := ext.GetMatchedText()
	site := link
	if u, err := url.Parse(link); err == nil && u.Hostname() != "" {
		site = u.Hostname()
	}
	return &backend.LinkPreview{
		URL: link, Site: site, Title: ext.GetTitle(), Description: ext.GetDescription(),
	}
}

// waInlineImage reports whether m is an image to draw in the chat; view-once
// images are only opened on purpose.
func waInlineImage(m *waE2E.Message) bool {
	img := m.GetImageMessage()
	return img != nil && !img.GetViewOnce() && img.GetFileLength() <= backend.MaxInlineImage
}

func (wa *WaBackend) extractMessageContent(m *waE2E.Message) (text, mediaLabel string, multiline bool) {
	if m == nil {
		return "", "", false
	}
	if m.GetConversation() != "" {
		text = m.GetConversation()
		multiline = strings.Contains(text, "\n")
		return text, "", multiline
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		text = ext.GetText()
		multiline = strings.Contains(text, "\n")
		return text, "", multiline
	}
	if img := m.GetImageMessage(); img != nil {
		text = img.GetCaption()
		mediaLabel = "[photo]"
		return
	}
	if vid := m.GetVideoMessage(); vid != nil {
		text = vid.GetCaption()
		mediaLabel = "[video]"
		return
	}
	if aud := m.GetAudioMessage(); aud != nil {
		secs := aud.GetSeconds()
		mediaLabel = fmt.Sprintf("[audio] %d:%02d", secs/60, secs%60)
		return
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		name := doc.GetFileName()
		if name == "" {
			name = "document"
		}
		mediaLabel = fmt.Sprintf("[file] %s", name)
		text = doc.GetTitle()
		return
	}
	if stk := m.GetStickerMessage(); stk != nil {
		mediaLabel = "[sticker]"
		return
	}
	if loc := m.GetLocationMessage(); loc != nil {
		mediaLabel = fmt.Sprintf("[location] %.4f, %.4f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
		text = loc.GetName()
		return
	}
	if contact := m.GetContactMessage(); contact != nil {
		mediaLabel = "[contact]"
		text = contact.GetDisplayName()
		return
	}
	if m.GetProtocolMessage() != nil {
		return "", "", false
	}
	return "[unsupported message type]", "", false
}

func extractReplyID(m *waE2E.Message) string {
	return replyContext(m).GetStanzaID()
}

// replyContext is the ContextInfo of the message kinds that can be replies.
func replyContext(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m == nil:
		return nil
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	}
	return nil
}

// quoteOf is the copy of the original a reply carries. WhatsApp cannot fetch
// a message by ID, and history holds only what the phone synced.
func (wa *WaBackend) quoteOf(m *waE2E.Message, name func(types.JID) string) *backend.Message {
	ci := replyContext(m)
	if ci.GetStanzaID() == "" || ci.GetQuotedMessage() == nil {
		return nil
	}
	text, mediaLabel, _ := wa.extractMessageContent(ci.GetQuotedMessage())
	text, _ = backend.WhatsApp(text)
	if text == "" && mediaLabel == "" {
		return nil
	}
	sender, err := types.ParseJID(ci.GetParticipant())
	if err != nil || sender.User == "" {
		return nil
	}
	return &backend.Message{
		ID: ci.GetStanzaID(), Sender: name(sender), Text: text, MediaLabel: mediaLabel,
		Outgoing: sender.User == wa.Jid.User,
	}
}

func isForwarded(m *waE2E.Message) bool {
	if m == nil {
		return false
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		if ci := ext.GetContextInfo(); ci != nil {
			return ci.GetIsForwarded()
		}
	}
	return false
}

// Profile describes a user from the contact store, their about text and,
// for a business, its business profile.
func (wa *WaBackend) Profile(ctx context.Context, userID string) (backend.Profile, error) {
	jid, err := types.ParseJID(userID)
	if err != nil {
		return backend.Profile{}, fmt.Errorf("read user %q: %w", userID, err)
	}
	jid = jid.ToNonAD()
	contact, err := wa.Client.Store.Contacts.GetContact(ctx, jid)
	if err != nil {
		return backend.Profile{}, fmt.Errorf("load contact: %w", err)
	}
	p := backend.Profile{Name: cmp.Or(contact.FullName, contact.PushName,
		contact.BusinessName, wa.lookupName(jid))}
	if contact.PushName != p.Name {
		p.Add("Profile name", contact.PushName)
	}
	switch {
	case jid.Server == types.DefaultUserServer:
		p.Add("Phone", "+"+jid.User)
	case contact.RedactedPhone != "":
		p.Add("Phone", contact.RedactedPhone)
	}
	info, err := wa.Client.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return backend.Profile{}, fmt.Errorf("load profile: %w", err)
	}
	user := info[jid]
	p.Add("About", user.Status)
	if user.VerifiedName == nil {
		return p, nil
	}
	if d := user.VerifiedName.Details; d != nil {
		p.Add("Business", d.GetVerifiedName())
	}
	// A business's profile is extra; the rest stands without it.
	if biz, err := wa.Client.GetBusinessProfile(ctx, jid); err == nil {
		var cats []string
		for _, c := range biz.Categories {
			cats = append(cats, c.Name)
		}
		p.Add("Category", strings.Join(cats, ", "))
		p.Add("Address", biz.Address)
		p.Add("Email", biz.Email)
	}
	return p, nil
}
