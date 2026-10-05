package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"golang.org/x/term"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

type waBackend struct {
	client    *whatsmeow.Client
	container *sqlstore.Container
	jid       types.JID
	pushName  string

	mu          sync.Mutex
	subscribed  types.JID
	pending     []waEvent
	changed     chan struct{}
	typing      map[types.JID]time.Time
	unreads     map[types.JID]int
	chatCache   []waChatEntry
	msgCache    map[types.JID][]message
	userCache   map[types.JID]string
	recentEmoji []string
	subs        subscriberSet

	configDir string
}

type waChatEntry struct {
	jid     types.JID
	name    string
	lastAt  time.Time
	unread  int
	isGroup bool
}

type waEvent struct {
	kind     string // "message", "edit", "delete", "reaction"
	msg      message
	editID   string
	targetID string
	reaction genericReactionUpdate
}

func newWABackend(configDir string) *waBackend {
	return &waBackend{
		changed:   make(chan struct{}, 1),
		typing:    make(map[types.JID]time.Time),
		unreads:   make(map[types.JID]int),
		chatCache: nil,
		msgCache:  make(map[types.JID][]message),
		userCache: make(map[types.JID]string),
		configDir: configDir,
	}
}

func (wa *waBackend) Name() string { return "whatsapp" }

func (wa *waBackend) Capabilities() capabilities {
	return capabilities{
		Forward:       true,
		DeleteForMe:   true,
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

func (wa *waBackend) openStore(ctx context.Context) error {
	dbPath := filepath.Join(wa.configDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite", "file:"+dbPath+"?_foreign_keys=on", nil)
	if err != nil {
		return fmt.Errorf("open WhatsApp session store: %w", err)
	}
	wa.container = container
	return nil
}

func (wa *waBackend) Authenticate(ctx context.Context, prompt promptFunc, report func(error)) error {
	if wa.container == nil {
		if err := wa.openStore(ctx); err != nil {
			return err
		}
	}
	deviceStore, err := wa.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load WhatsApp device: %w", err)
	}
	wa.client = whatsmeow.NewClient(deviceStore, nil)
	wa.client.AddEventHandler(wa.handleEvent)

	if wa.client.Store.ID != nil {
		if err := wa.client.Connect(); err != nil {
			return fmt.Errorf("reconnect to WhatsApp: %w", err)
		}
		wa.jid = *wa.client.Store.ID
		wa.pushName = wa.client.Store.PushName
		return nil
	}

	if prompt == nil {
		return fmt.Errorf("WhatsApp account needs pairing")
	}

	qrChan, err := wa.client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := wa.client.Connect(); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}

	// Wait for QR event before requesting pair code.
	select {
	case <-ctx.Done():
		wa.client.Disconnect()
		return ctx.Err()
	case item := <-qrChan:
		if item.Event != "code" {
			wa.client.Disconnect()
			return fmt.Errorf("unexpected pairing event: %s", item.Event)
		}
	}

	phone, err := prompt("Phone number (international, e.g. +91...): ", false)
	if err != nil {
		wa.client.Disconnect()
		return err
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		wa.client.Disconnect()
		return fmt.Errorf("phone number is required")
	}

	code, err := wa.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		wa.client.Disconnect()
		return fmt.Errorf("request pairing code: %w", err)
	}
	report(fmt.Errorf("Enter this code on your phone: %s-%s", code[:4], code[4:]))

	for item := range qrChan {
		switch item.Event {
		case "success":
			wa.jid = *wa.client.Store.ID
			wa.pushName = wa.client.Store.PushName
			return nil
		case "timeout":
			wa.client.Disconnect()
			return fmt.Errorf("pairing timed out; try again")
		}
	}
	wa.client.Disconnect()
	return fmt.Errorf("pairing failed")
}

func (wa *waBackend) Identity() accountIdentity {
	return accountIdentity{
		Name:  wa.pushName,
		Phone: wa.jid.User,
	}
}

func (wa *waBackend) AccountLabel() string {
	if wa.jid.IsEmpty() {
		return "[WA] awaiting sign-in"
	}
	phone := wa.jid.User
	if wa.pushName != "" {
		return fmt.Sprintf("[WA] %s (+%s)", plain(wa.pushName), phone)
	}
	return fmt.Sprintf("[WA] +%s", phone)
}

func (wa *waBackend) AutoLockEnabled() bool { return false }

func (wa *waBackend) Run(ctx context.Context) error {
	<-ctx.Done()
	if wa.client != nil {
		wa.client.Disconnect()
	}
	return ctx.Err()
}

// Event handler.

func (wa *waBackend) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		wa.handleMessage(evt)
	case *events.Receipt:
		wa.handleReceipt(evt)
	case *events.ChatPresence:
		wa.handlePresence(evt)
	case *events.HistorySync:
		wa.handleHistorySync(evt)
	case *events.Connected:
		_ = wa.client.SendPresence(context.Background(), types.PresenceAvailable)
	case *events.LoggedOut:
		wa.mu.Lock()
		wa.chatCache = nil
		wa.mu.Unlock()
	}
}

func (wa *waBackend) handleMessage(evt *events.Message) {
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

	if evt.Info.PushName != "" {
		wa.mu.Lock()
		wa.userCache[evt.Info.Sender.ToNonAD()] = evt.Info.PushName
		wa.mu.Unlock()
	}

	chat := chatID{backend: "whatsapp", id: chatJID.String()}

	wa.mu.Lock()
	wa.touchChat(chatJID, evt.Info.Timestamp)

	if evt.IsEdit {
		if wa.subscribed == chatJID {
			wa.pending = append(wa.pending, waEvent{
				kind:   "edit",
				msg:    msg,
				editID: getEditTargetID(evt),
			})
			wa.doNotify()
		}
		wa.mu.Unlock()
		wa.subs.broadcast(backendEvent{Kind: "edit", Chat: chat, Message: &msg})
		return
	}

	if evt.Message.GetReactionMessage() != nil {
		reaction := evt.Message.GetReactionMessage()
		targetID := reaction.GetKey().GetID()
		emoji := reaction.GetText()
		if wa.subscribed == chatJID {
			wa.pending = append(wa.pending, waEvent{
				kind: "reaction",
				reaction: genericReactionUpdate{
					MessageID: targetID,
				},
			})
			wa.doNotify()
		}
		_ = emoji
		wa.mu.Unlock()
		return
	}

	if !evt.Info.IsFromMe {
		wa.unreads[chatJID]++
	}

	if wa.subscribed == chatJID {
		wa.pending = append(wa.pending, waEvent{kind: "message", msg: msg})
		wa.doNotify()
	}
	wa.mu.Unlock()

	wa.subs.broadcast(backendEvent{Kind: "message", Chat: chat, Message: &msg})
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

func (wa *waBackend) handleReceipt(evt *events.Receipt) {
	if evt.Type != types.ReceiptTypeRead && evt.Type != types.ReceiptTypeReadSelf {
		return
	}
	if evt.Type == types.ReceiptTypeReadSelf {
		chatJID := evt.Chat
		wa.mu.Lock()
		wa.unreads[chatJID] = 0
		wa.mu.Unlock()
		wa.notify()
	}
}

func (wa *waBackend) handlePresence(evt *events.ChatPresence) {
	wa.mu.Lock()
	if evt.State == types.ChatPresenceComposing {
		wa.typing[evt.Sender] = time.Now()
	} else {
		delete(wa.typing, evt.Sender)
	}
	sub := wa.subscribed
	wa.mu.Unlock()
	if evt.Chat == sub {
		wa.notify()
	}
}

func (wa *waBackend) handleHistorySync(evt *events.HistorySync) {
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
		unread := int(conv.GetUnreadCount())
		isGroup := jid.Server == types.GroupServer

		found := false
		for i := range wa.chatCache {
			if wa.chatCache[i].jid == jid {
				wa.chatCache[i].name = name
				if !lastAt.IsZero() {
					wa.chatCache[i].lastAt = lastAt
				}
				wa.chatCache[i].unread = unread
				found = true
				break
			}
		}
		if !found {
			wa.chatCache = append(wa.chatCache, waChatEntry{
				jid:     jid,
				name:    name,
				lastAt:  lastAt,
				unread:  unread,
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

func (wa *waBackend) historySyncMessages(chatJID types.JID, hsmsgs []*waHistorySync.HistorySyncMsg) []message {
	var result []message
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
		result = append(result, msg)
	}
	return result
}

func (wa *waBackend) touchChat(chatJID types.JID, ts time.Time) {
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

func (wa *waBackend) contactName(jid types.JID) string {
	nonAD := jid.ToNonAD()
	if name, ok := wa.userCache[nonAD]; ok && name != "" {
		return name
	}
	if jid.Server == types.GroupServer {
		return jid.User
	}
	return "+" + jid.User
}

// Chat list.

func (wa *waBackend) Dialogs(ctx context.Context, archived bool) ([]chatEntry, int, error) {
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

	entries := make([]chatEntry, 0, len(cache))
	for _, c := range cache {
		title := c.name
		if title == "" {
			title = wa.contactName(c.jid)
		}
		isDM := c.jid.Server == types.DefaultUserServer
		entries = append(entries, chatEntry{
			ID:      chatID{backend: "whatsapp", id: c.jid.String()},
			Title:   title,
			Unread:  c.unread,
			LastAt:  c.lastAt,
			IsDM:    isDM,
			IsGroup: c.isGroup,
			Raw:     c.jid,
		})
	}
	return entries, 0, nil
}

// Message history.

func (wa *waBackend) History(ctx context.Context, ch chatEntry) ([]message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return nil, err
	}
	wa.mu.Lock()
	cached := wa.msgCache[jid]
	wa.mu.Unlock()
	if len(cached) > 50 {
		cached = cached[len(cached)-50:]
	}
	result := make([]message, len(cached))
	copy(result, cached)
	return result, nil
}

func (wa *waBackend) OlderHistory(ctx context.Context, ch chatEntry, beforeID string) ([]message, error) {
	return nil, nil
}

// Sending.

func (wa *waBackend) Send(ctx context.Context, ch chatEntry, text string, replyTo string) (message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return message{}, err
	}
	msg := &waE2E.Message{}
	if replyTo != "" {
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      proto.String(replyTo),
				Participant:   proto.String(jid.String()),
				QuotedMessage: &waE2E.Message{Conversation: proto.String("")},
			},
		}
	} else {
		msg.Conversation = proto.String(text)
	}
	resp, err := wa.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return message{}, fmt.Errorf("send message: %w", err)
	}
	return message{
		ID:        resp.ID,
		Sender:    "you",
		Text:      text,
		Timestamp: resp.Timestamp,
		Outgoing:  true,
		ReplyToID: replyTo,
	}, nil
}

func (wa *waBackend) SendUpload(ctx context.Context, ch chatEntry, filePath, caption, replyTo string) (message, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return message{}, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return message{}, fmt.Errorf("read file: %w", err)
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
	uploaded, err := wa.client.Upload(ctx, data, mediaType)
	if err != nil {
		return message{}, fmt.Errorf("upload file: %w", err)
	}
	var waMsg *waE2E.Message
	var ctxInfo *waE2E.ContextInfo
	if replyTo != "" {
		ctxInfo = &waE2E.ContextInfo{
			StanzaID:      proto.String(replyTo),
			Participant:   proto.String(jid.String()),
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
	resp, err := wa.client.SendMessage(ctx, jid, waMsg)
	if err != nil {
		return message{}, fmt.Errorf("send media: %w", err)
	}
	label := mediaLabelForMIME(mimeType, fileName, int64(uploaded.FileLength))
	return message{
		ID:         resp.ID,
		Sender:     "you",
		Text:       caption,
		MediaLabel: label,
		Timestamp:  resp.Timestamp,
		Outgoing:   true,
		ReplyToID:  replyTo,
	}, nil
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
	sizeStr := formatSize(size)
	return fmt.Sprintf("[%s] %s, %s", kind, fileName, sizeStr)
}

func (wa *waBackend) UploadPlaceholder(path, caption string) message {
	label := "[uploading] " + filepath.Base(path)
	return message{
		ID:         "upload-placeholder",
		Sender:     "you",
		Text:       caption,
		MediaLabel: label,
		Timestamp:  time.Now(),
		Outgoing:   true,
	}
}

func (wa *waBackend) Editable(msg message) error {
	return genericEditable(msg)
}

func (wa *waBackend) Edit(ctx context.Context, ch chatEntry, msg message, text string) error {
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
	_, err = wa.client.SendMessage(ctx, jid, editMsg)
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	return nil
}

func (wa *waBackend) DeleteMessages(ctx context.Context, ch chatEntry, ids []string, forEveryone bool) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if forEveryone {
			_, err := wa.client.RevokeMessage(ctx, jid, id)
			if err != nil {
				return fmt.Errorf("revoke message: %w", err)
			}
		} else {
			delMsg := &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Key: &waCommon.MessageKey{
						RemoteJID: proto.String(jid.String()),
						FromMe:    proto.Bool(true),
						ID:        proto.String(id),
					},
					Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				},
			}
			_, err := wa.client.SendMessage(ctx, jid, delMsg)
			if err != nil {
				return fmt.Errorf("delete message: %w", err)
			}
		}
	}
	return nil
}

func (wa *waBackend) DeleteChat(ctx context.Context, ch chatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported for WhatsApp")
}

func (wa *waBackend) React(ctx context.Context, ch chatEntry, msg message, emoji string) ([]messageReaction, error) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return nil, err
	}
	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(jid.String()),
				FromMe:    proto.Bool(msg.Outgoing),
				ID:        proto.String(msg.ID),
			},
			Text:              proto.String(emoji),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}
	_, err = wa.client.SendMessage(ctx, jid, reactionMsg)
	if err != nil {
		return nil, fmt.Errorf("send reaction: %w", err)
	}
	updated := make([]messageReaction, len(msg.Reactions))
	copy(updated, msg.Reactions)
	found := false
	for i, r := range updated {
		if r.Chosen {
			if r.Emoji == emoji {
				updated[i].Count--
				updated[i].Chosen = false
				if updated[i].Count <= 0 {
					updated = append(updated[:i], updated[i+1:]...)
				}
				found = true
				break
			}
			updated[i].Count--
			updated[i].Chosen = false
			if updated[i].Count <= 0 {
				updated = append(updated[:i], updated[i+1:]...)
			}
			break
		}
	}
	if !found {
		added := false
		for i, r := range updated {
			if r.Emoji == emoji {
				updated[i].Count++
				updated[i].Chosen = true
				added = true
				break
			}
		}
		if !added {
			updated = append(updated, messageReaction{Emoji: emoji, Count: 1, Chosen: true})
		}
	}
	return updated, nil
}

func (wa *waBackend) MarkRead(ctx context.Context, ch chatEntry, upToID string) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	err = wa.client.MarkRead(ctx, []types.MessageID{upToID}, time.Now(), jid, types.EmptyJID)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	wa.mu.Lock()
	wa.unreads[jid] = 0
	wa.mu.Unlock()
	return nil
}

func (wa *waBackend) SetTyping(ctx context.Context, ch chatEntry) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return
	}
	_ = wa.client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText)
}

func (wa *waBackend) Forward(ctx context.Context, from chatEntry, to chatEntry, msgID string) error {
	toJID, err := wa.chatJID(to)
	if err != nil {
		return err
	}
	var original *message
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
	_, err = wa.client.SendMessage(ctx, toJID, msg)
	if err != nil {
		return fmt.Errorf("forward message: %w", err)
	}
	return nil
}

func (wa *waBackend) Pin(ctx context.Context, ch chatEntry, msg message, unpin bool) error {
	return fmt.Errorf("pinning is not supported for WhatsApp")
}

func (wa *waBackend) SearchMessages(ctx context.Context, ch chatEntry, query string) ([]message, error) {
	return nil, fmt.Errorf("search is not available for WhatsApp")
}

// Subscribe/Unsubscribe.

func (wa *waBackend) Subscribe(ch chatEntry) {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return
	}
	wa.mu.Lock()
	wa.subscribed = jid
	wa.pending = nil
	wa.mu.Unlock()
}

func (wa *waBackend) Unsubscribe() {
	wa.mu.Lock()
	wa.subscribed = types.EmptyJID
	wa.pending = nil
	wa.mu.Unlock()
}

func (wa *waBackend) Drain() ([]message, []genericReactionUpdate, []string) {
	wa.mu.Lock()
	events := wa.pending
	wa.pending = nil
	wa.mu.Unlock()
	var msgs []message
	var reactions []genericReactionUpdate
	var deletions []string
	for _, e := range events {
		switch e.kind {
		case "message":
			msgs = append(msgs, e.msg)
		case "edit":
			msgs = append(msgs, e.msg)
		case "delete":
			deletions = append(deletions, e.targetID)
		case "reaction":
			reactions = append(reactions, e.reaction)
		}
	}
	return msgs, reactions, deletions
}

func (wa *waBackend) Changed() <-chan struct{} { return wa.changed }

func (wa *waBackend) AddSubscriber(id string, filter func(chatID) bool, bufSize int) <-chan backendEvent {
	return wa.subs.add(id, filter, bufSize)
}

func (wa *waBackend) RemoveSubscriber(id string) {
	wa.subs.remove(id)
}

func (wa *waBackend) IsTyping() bool {
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

func (wa *waBackend) ClearTyping() {
	wa.mu.Lock()
	wa.typing = make(map[types.JID]time.Time)
	wa.mu.Unlock()
}

// Read state.

func (wa *waBackend) ReadMarker(ch chatEntry, msg message) string              { return "" }
func (wa *waBackend) RefreshReadState(ctx context.Context, ch chatEntry) error { return nil }
func (wa *waBackend) RefreshChatUnread(ctx context.Context, ch chatEntry) (int, error) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	jid, err := wa.chatJID(ch)
	if err != nil {
		return 0, err
	}
	return wa.unreads[jid], nil
}

func (wa *waBackend) CachedUnread(ch chatEntry) int {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	jid, _ := wa.chatJID(ch)
	return wa.unreads[jid]
}

func (wa *waBackend) InboxPosition(ch chatEntry) string { return "" }

// Media.

func (wa *waBackend) MediaPlayable(msg message) bool {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return false
	}
	m := evt.Message
	return m.GetImageMessage() != nil || m.GetVideoMessage() != nil ||
		m.GetAudioMessage() != nil || m.GetStickerMessage() != nil
}

func (wa *waBackend) PlayMedia(ctx context.Context, t *terminal, msg message, player string) (result error) {
	evt, ok := msg.Raw.(*events.Message)
	if !ok {
		return fmt.Errorf("this message has no playable media")
	}
	var dl whatsmeow.DownloadableMessage
	var filename string
	var isAudio bool
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
		isAudio = true
	case m.GetStickerMessage() != nil:
		dl = m.GetStickerMessage()
		filename = "sticker.webp"
	default:
		return fmt.Errorf("this message has no playable media")
	}
	data, err := wa.client.Download(ctx, dl)
	if err != nil {
		return fmt.Errorf("download media: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "wiregram-wa-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if e := os.RemoveAll(tmpDir); e != nil {
			result = fmt.Errorf("remove temporary media: %w", e)
		}
	}()
	dest := filepath.Join(tmpDir, filename)
	if err := os.WriteFile(dest, data, 0600); err != nil {
		return fmt.Errorf("write media file: %w", err)
	}
	args := []string{"--no-config", "--load-scripts=no", "--input-terminal=no",
		"--input-default-bindings=no", "--input-media-keys=no", "--osc=no",
		"--osd-level=0", "--really-quiet", "--audio-display=no",
		"--autoload-files=no", "--image-display-duration=inf"}
	if isAudio {
		args = append(args, "--vid=no", "--vo=null")
	} else {
		cols, rows, termErr := term.GetSize(int(os.Stdout.Fd()))
		if termErr != nil || cols < 1 || rows < 4 {
			cols, rows = 80, 24
		}
		args = append(args, "--profile=sw-fast", "--vo=kitty",
			"--vo-kitty-alt-screen=no", "--vo-kitty-use-shm=no",
			"--vo-kitty-config-clear=no", "--vo-kitty-top=3", "--vo-kitty-left=1",
			fmt.Sprintf("--vo-kitty-cols=%d", cols),
			fmt.Sprintf("--vo-kitty-rows=%d", rows-2),
			fmt.Sprintf("--vo-kitty-width=%d", cols*8),
			fmt.Sprintf("--vo-kitty-height=%d", (rows-2)*16))
	}
	args = append(args, "--", dest)
	t.mediaScreen(filename)
	defer t.clearMedia()
	cmd := exec.CommandContext(ctx, player, args...)
	cmd.Stdout = mediaOutput{t}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("media playback failed; check that mpv supports this format and the Kitty video output: %w", err)
	}
	return nil
}

func (wa *waBackend) MediaExec(ctx context.Context, msg message, player string) (*exec.Cmd, func(), error) {
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
	data, err := wa.client.Download(ctx, dl)
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
	return mpvCmd(ctx, player, isAudio, dest), cleanup, nil
}

func (wa *waBackend) MediaSaveable(msg message) (string, bool) {
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
		name := m.GetDocumentMessage().GetFileName()
		if name == "" {
			name = "document"
		}
		return name, true
	case m.GetStickerMessage() != nil:
		return "sticker.webp", true
	}
	return "", false
}

func (wa *waBackend) SaveMedia(ctx context.Context, msg message, destDir string) error {
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
		filename = m.GetDocumentMessage().GetFileName()
		if filename == "" {
			filename = "document"
		}
	case m.GetStickerMessage() != nil:
		dl = m.GetStickerMessage()
		filename = "sticker.webp"
	default:
		return fmt.Errorf("this message has no downloadable attachment")
	}
	data, err := wa.client.Download(ctx, dl)
	if err != nil {
		return fmt.Errorf("download media: %w", err)
	}
	dest := filepath.Join(destDir, filename)
	if err := os.WriteFile(dest, data, 0600); err != nil {
		return fmt.Errorf("save media: %w", err)
	}
	return nil
}

func (wa *waBackend) AcknowledgeMedia(ctx context.Context, msg message) {}

// User search.

func (wa *waBackend) SearchUsers(ctx context.Context, t *terminal, recentChats []chatEntry) (chatEntry, bool, error) {
	var base []struct {
		jid   types.JID
		label string
		match string
	}
	for _, ch := range recentChats {
		if !ch.IsDM {
			continue
		}
		jid, ok := ch.Raw.(types.JID)
		if !ok {
			continue
		}
		label := ch.Title
		base = append(base, struct {
			jid   types.JID
			label string
			match string
		}{jid: jid, label: label, match: strings.ToLower(label + " " + jid.User)})
	}

	choices := base
	selected := 0
	var query []rune
	heading := "Recent DMs"

	render := func() {
		labels := make([]userChoice, len(choices))
		for i, c := range choices {
			labels[i] = userChoice{label: c.label}
		}
		renderUserSearch(t.out(), labels, selected, string(query), heading, "", false)
	}
	render()

	for {
		select {
		case <-ctx.Done():
			return chatEntry{}, false, ctx.Err()
		case key, ok := <-t.keys:
			if !ok {
				return chatEntry{}, false, context.Canceled
			}
			switch key {
			case "\x1b", "\x03", "\x04":
				return chatEntry{}, false, nil
			case "\r", "\n":
				if selected < len(choices) {
					c := choices[selected]
					return chatEntry{
						ID:    chatID{backend: "whatsapp", id: c.jid.String()},
						Title: c.label,
						IsDM:  true,
						Raw:   c.jid,
					}, true, nil
				}
			case "k", "\x1b[A", "\x1bOA":
				if selected > 0 {
					selected--
				}
			case "j", "\x1b[B", "\x1bOB":
				if selected < len(choices)-1 {
					selected++
				}
			case "\x7f", "\b":
				if len(query) > 0 {
					query = query[:len(query)-1]
				}
				choices = filterWAChoices(base, string(query))
				if len(choices) == 0 && len(query) == 0 {
					choices = base
				}
				selected = 0
			default:
				if len(key) == 1 && key[0] >= 32 {
					query = append(query, rune(key[0]))
					choices = filterWAChoices(base, string(query))
					selected = 0
				}
			}
			render()
		}
	}
}

type waUserChoice struct {
	jid   types.JID
	label string
	match string
}

func filterWAChoices(all []struct {
	jid   types.JID
	label string
	match string
}, q string) []struct {
	jid   types.JID
	label string
	match string
} {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	var out []struct {
		jid   types.JID
		label string
		match string
	}
	for _, c := range all {
		if strings.Contains(c.match, q) {
			out = append(out, c)
		}
	}
	return out
}

func (wa *waBackend) HandleForward(ctx context.Context, t *terminal, recentChats []chatEntry, fromChat chatEntry, msgID string) {
	target, ok, err := wa.SearchUsers(ctx, t, recentChats)
	if err != nil || !ok {
		return
	}
	if err := wa.Forward(ctx, fromChat, target, msgID); err != nil {
		t.report(err)
	}
}

// Emoji.

func (wa *waBackend) RecentEmoji() []string { return wa.recentEmoji }

func (wa *waBackend) RememberEmoji(value string) {
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

func (wa *waBackend) EmojiChoices(query string) []emojiOption {
	catalog := emojiCatalog()
	if query == "" {
		recent := wa.recentEmoji
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

func (wa *waBackend) ReactionChoices(query string) []emojiOption {
	return wa.EmojiChoices(query)
}

// Helpers.

func (wa *waBackend) chatJID(ch chatEntry) (types.JID, error) {
	if jid, ok := ch.Raw.(types.JID); ok {
		return jid, nil
	}
	jid, err := types.ParseJID(ch.ID.id)
	if err != nil {
		return types.EmptyJID, fmt.Errorf("parse chat JID: %w", err)
	}
	return jid, nil
}

func (wa *waBackend) notify() {
	select {
	case wa.changed <- struct{}{}:
	default:
	}
}

func (wa *waBackend) doNotify() {
	select {
	case wa.changed <- struct{}{}:
	default:
	}
}

func (wa *waBackend) eventMessageToMessage(evt *events.Message) message {
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
	text, mediaLabel, multiline := wa.extractMessageContent(evt.Message)
	return message{
		ID:         info.ID,
		Sender:     senderName,
		SenderID:   info.Sender.String(),
		Text:       text,
		Timestamp:  info.Timestamp,
		Outgoing:   outgoing,
		MediaLabel: mediaLabel,
		Multiline:  multiline,
		ReplyToID:  extractReplyID(evt.Message),
		Forwarded:  isForwarded(evt.Message),
		Raw:        evt,
	}
}

func (wa *waBackend) webMessageToMessage(chatJID types.JID, wmi *waWeb.WebMessageInfo) message {
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
	text, mediaLabel, multiline := wa.extractMessageContent(m)
	return message{
		ID:         key.GetID(),
		Sender:     senderName,
		Text:       text,
		Timestamp:  ts,
		Outgoing:   outgoing,
		MediaLabel: mediaLabel,
		Multiline:  multiline,
		ReplyToID:  extractReplyID(m),
		Forwarded:  isForwarded(m),
	}
}

func (wa *waBackend) extractMessageContent(m *waE2E.Message) (text, mediaLabel string, multiline bool) {
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
	if m == nil {
		return ""
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		if ci := ext.GetContextInfo(); ci != nil {
			return ci.GetStanzaID()
		}
	}
	if img := m.GetImageMessage(); img != nil {
		if ci := img.GetContextInfo(); ci != nil {
			return ci.GetStanzaID()
		}
	}
	if vid := m.GetVideoMessage(); vid != nil {
		if ci := vid.GetContextInfo(); ci != nil {
			return ci.GetStanzaID()
		}
	}
	if aud := m.GetAudioMessage(); aud != nil {
		if ci := aud.GetContextInfo(); ci != nil {
			return ci.GetStanzaID()
		}
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		if ci := doc.GetContextInfo(); ci != nil {
			return ci.GetStanzaID()
		}
	}
	return ""
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
