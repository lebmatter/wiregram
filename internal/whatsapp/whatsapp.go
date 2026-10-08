package whatsapp

import (
	"context"
	"fmt"
	"mime"
	"net/url"
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

	mu          sync.Mutex
	typing      map[types.JID]time.Time
	unreads     map[types.JID]int
	chatCache   []waChatEntry
	msgCache    map[types.JID][]backend.Message
	userCache   map[types.JID]string
	recentEmoji []string
	events      backend.Bus

	configDir string
}

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
		configDir: configDir,
	}
}

func (wa *WaBackend) Name() string { return "whatsapp" }

func (wa *WaBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
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

func (wa *WaBackend) openStore(ctx context.Context) error {
	dbPath := filepath.Join(wa.configDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite", "file:"+dbPath+"?_foreign_keys=on", nil)
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
	report(fmt.Errorf("Enter this code on your phone: %s-%s", code[:4], code[4:]))

	for item := range qrChan {
		switch item.Event {
		case "success":
			wa.Jid = *wa.Client.Store.ID
			wa.PushName = wa.Client.Store.PushName
			return nil
		case "timeout":
			wa.Client.Disconnect()
			return fmt.Errorf("pairing timed out; try again")
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

func (wa *WaBackend) AutoLockEnabled() bool { return false }

func (wa *WaBackend) Logout(ctx context.Context) error {
	if err := wa.Client.Logout(ctx); err != nil {
		return fmt.Errorf("unlink WhatsApp device: %w", err)
	}
	return nil
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
	case *events.Connected:
		_ = wa.Client.SendPresence(context.Background(), types.PresenceAvailable)
	case *events.LoggedOut:
		wa.mu.Lock()
		wa.chatCache = nil
		wa.mu.Unlock()
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

	if evt.Info.PushName != "" {
		wa.mu.Lock()
		wa.userCache[evt.Info.Sender.ToNonAD()] = evt.Info.PushName
		wa.mu.Unlock()
	}

	chat := backend.ChatID{Backend: "whatsapp", Id: chatJID.String()}

	wa.mu.Lock()
	wa.touchChat(chatJID, evt.Info.Timestamp)
	reaction := evt.Message.GetReactionMessage()
	if !evt.IsEdit && reaction == nil && !evt.Info.IsFromMe {
		wa.unreads[chatJID]++
	}
	// Person-to-person only: not groups, broadcasts or the own number.
	dm := (chatJID.Server == types.DefaultUserServer || chatJID.Server == types.HiddenUserServer) &&
		chatJID.User != wa.Jid.User
	wa.mu.Unlock()

	switch {
	case evt.IsEdit:
		msg.ID = getEditTargetID(evt)
		wa.events.Publish(backend.Event{Kind: backend.EventEdit, Chat: chat, DM: dm, Message: &msg})
	case reaction != nil:
		wa.events.Publish(backend.Event{
			Kind: backend.EventReaction, Chat: chat,
			Reaction: &backend.GenericReactionUpdate{MessageID: reaction.GetKey().GetID()},
		})
	default:
		wa.events.Publish(backend.Event{Kind: backend.EventMessage, Chat: chat, DM: dm, Message: &msg})
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
		result = append(result, msg)
	}
	return result
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
		title := c.name
		if title == "" {
			title = wa.contactName(c.jid)
		}
		isDM := c.jid.Server == types.DefaultUserServer
		entries = append(entries, backend.ChatEntry{
			ID:      backend.ChatID{Backend: "whatsapp", Id: c.jid.String()},
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

func (wa *WaBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
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
	result := make([]backend.Message, len(cached))
	copy(result, cached)
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
	resp, err := wa.Client.SendMessage(ctx, jid, msg)
	if err != nil {
		return backend.Message{}, fmt.Errorf("send message: %w", err)
	}
	return backend.Message{
		ID:        resp.ID,
		Sender:    "you",
		Text:      text,
		Timestamp: resp.Timestamp,
		Outgoing:  true,
		ReplyToID: replyTo,
	}, nil
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
	resp, err := wa.Client.SendMessage(ctx, jid, waMsg)
	if err != nil {
		return backend.Message{}, fmt.Errorf("send media: %w", err)
	}
	label := mediaLabelForMIME(mimeType, fileName, int64(uploaded.FileLength))
	return backend.Message{
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
	for _, id := range ids {
		if forEveryone {
			_, err := wa.Client.RevokeMessage(ctx, jid, id)
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
			_, err := wa.Client.SendMessage(ctx, jid, delMsg)
			if err != nil {
				return fmt.Errorf("delete message: %w", err)
			}
		}
	}
	return nil
}

func (wa *WaBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("chat deletion is not supported for WhatsApp")
}

func (wa *WaBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
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
	_, err = wa.Client.SendMessage(ctx, jid, reactionMsg)
	if err != nil {
		return nil, fmt.Errorf("send reaction: %w", err)
	}
	updated := make([]backend.MessageReaction, len(msg.Reactions))
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
			updated = append(updated, backend.MessageReaction{Emoji: emoji, Count: 1, Chosen: true})
		}
	}
	return updated, nil
}

func (wa *WaBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	jid, err := wa.chatJID(ch)
	if err != nil {
		return err
	}
	err = wa.Client.MarkRead(ctx, []types.MessageID{upToID}, time.Now(), jid, types.EmptyJID)
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
	data, err := wa.Client.Download(ctx, dl)
	if err != nil {
		return fmt.Errorf("download media: %w", err)
	}
	dest := filepath.Join(destDir, filename)
	if err := os.WriteFile(dest, data, 0600); err != nil {
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

func (wa *WaBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	entry, ok := c.Raw.(backend.ChatEntry)
	if !ok {
		return backend.ChatEntry{}, fmt.Errorf("open chat: unexpected search result")
	}
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
		recent := wa.recentEmoji
		if len(recent) == 0 {
			if len(catalog) > 10 {
				catalog = catalog[:10]
			}
			return catalog
		}
		var choices []backend.EmojiOption
		for _, v := range recent {
			choices = append(choices, backend.EmojiOption{Value: v, Name: v})
		}
		return choices
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
	text, mediaLabel, multiline := wa.extractMessageContent(evt.Message)
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
		Forwarded:  isForwarded(evt.Message),
		Preview:    waPreview(evt.Message),
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
	text, mediaLabel, multiline := wa.extractMessageContent(m)
	return backend.Message{
		ID:         key.GetID(),
		Sender:     senderName,
		Text:       text,
		Timestamp:  ts,
		Outgoing:   outgoing,
		MediaLabel: mediaLabel,
		Multiline:  multiline,
		ReplyToID:  extractReplyID(m),
		Forwarded:  isForwarded(m),
		Preview:    waPreview(m),
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
