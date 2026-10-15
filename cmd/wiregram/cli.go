package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/config"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

// commands are the subcommands that run without the TUI.
var commands = map[string]func(ctx context.Context, args []string) error{
	"reset":    cmdReset,
	"accounts": cmdAccounts,
	"chats":    cmdChats,
	"stream":   cmdStream,
	"send":     cmdSend,
	"dry-run":  cmdDryRun,
}

// connectTimeout bounds how long a command waits for an account to come online.
const connectTimeout = 30 * time.Second

// cliSession is the data a command works on, opened as the TUI opens it but
// without agents, plugins or the inbox watcher.
type cliSession struct {
	dir string
	db  *store.DB
	mgr *account.AccountManager
}

func openSession() (*cliSession, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := account.ImportLegacy(db, dir); err != nil {
		db.Close()
		return nil, err
	}
	cfg, err := config.LoadConfig(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	mgr, err := account.LoadAccounts(cfg, dir, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &cliSession{dir: dir, db: db, mgr: mgr}, nil
}

func (s *cliSession) Close() {
	s.mgr.Close()
	s.db.Close()
}

// backendType turns a backend name or its tag into the account BackendType.
func backendType(name string) (string, error) {
	switch strings.ToLower(name) {
	case "telegram", "tg":
		return "telegram", nil
	case "mattermost", "mm":
		return "mattermost", nil
	case "matrix", "mx":
		return "matrix", nil
	case "email", "em", "mail":
		return "email", nil
	case "whatsapp", "wa":
		return "", fmt.Errorf("WhatsApp is not available from the command line; " +
			"it would disconnect the running app")
	}
	return "", fmt.Errorf("unknown backend %q; use telegram, mattermost, matrix or email", name)
}

// accountNames are the ways an account can be named on the command line.
func accountNames(a *account.Account) []string {
	handle := a.Handle()
	names := []string{handle}
	switch a.BackendType {
	case "telegram":
		names = append(names, strings.TrimPrefix(handle, "+"), a.Snapshot().Identity.Name)
	case "mattermost":
		names = append(names, "@"+handle)
	case "matrix":
		local, _, _ := strings.Cut(strings.TrimPrefix(handle, "@"), ":")
		names = append(names, local)
	}
	return names
}

func findAccount(mgr *account.AccountManager, backendName, name string) (*account.Account, error) {
	typ, err := backendType(backendName)
	if err != nil {
		return nil, err
	}
	var found *account.Account
	for _, a := range mgr.List() {
		if a.BackendType != typ {
			continue
		}
		for _, n := range accountNames(a) {
			if n != "" && strings.EqualFold(n, name) {
				if found != nil && found != a {
					return nil, fmt.Errorf("%s matches more than one %s account", name, typ)
				}
				found = a
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no %s account %s; wiregram accounts lists them", typ, name)
	}
	return found, nil
}

// connect starts a and waits until it is online.
func connect(ctx context.Context, mgr *account.AccountManager, a *account.Account) (backend.Backend, error) {
	if s := a.Snapshot(); s.NeedsLogin || a.NeverAuthenticated() {
		return nil, fmt.Errorf("sign in to %s in wiregram first", a.Handle())
	}
	mgr.Start(ctx, a, nil)
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	for {
		changed := mgr.Changes()
		s := a.Snapshot()
		switch {
		case s.Status == "online" && s.Backend != nil:
			return s.Backend, nil
		case s.NeedsLogin:
			return nil, fmt.Errorf("sign in to %s in wiregram again", a.Handle())
		case s.Status == "offline" && s.Notice != "":
			return nil, fmt.Errorf("%s: %s", a.Handle(), s.Notice)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("%s did not connect in time", a.Handle())
			}
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// chatKind is dm, group or channel; Telegram marks channels only by title.
func chatKind(e backend.ChatEntry) (kind, title string) {
	title = textfmt.Plain(e.Title)
	if e.IsDM {
		return "dm", title
	}
	if e.ID.Backend == "email" {
		return "folder", title // email's chats are its folders
	}
	if rest, ok := strings.CutPrefix(title, "Channel: "); ok {
		return "channel", rest
	}
	return "group", title
}

// findChat resolves a recipient by chat ID or title among the chats, then
// through the backend's user search, since a DM's title is the person's
// display name rather than their username.
func findChat(ctx context.Context, b backend.Backend, entries []backend.ChatEntry,
	recipient string) (backend.ChatEntry, error) {
	for _, e := range entries {
		if e.ID.Id == recipient {
			return e, nil
		}
	}
	var matches []backend.ChatEntry
	for _, e := range entries {
		_, title := chatKind(e)
		if strings.EqualFold(title, recipient) || strings.EqualFold(textfmt.Plain(e.Title), recipient) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return backend.ChatEntry{}, fmt.Errorf("%s matches more than one chat; use its chat ID", recipient)
	}
	if c, ok, err := searchUser(ctx, b, recipient); err != nil {
		return backend.ChatEntry{}, err
	} else if ok {
		return b.OpenUserCandidate(ctx, c)
	}
	return backend.ChatEntry{}, fmt.Errorf("no chat or user %s; wiregram chats lists them", recipient)
}

// searchUser finds the one user search result named exactly by recipient:
// a username, a Matrix ID, an email address or the shown name.
func searchUser(ctx context.Context, b backend.Backend, recipient string) (backend.UserCandidate, bool, error) {
	query := strings.TrimLeft(recipient, "@~")
	results, err := b.UserSearchRemote(ctx, query)
	if err != nil {
		return backend.UserCandidate{}, false, fmt.Errorf("search %s: %w", recipient, err)
	}
	want := strings.ToLower(query)
	var found []backend.UserCandidate
	for _, c := range results {
		exact := strings.EqualFold(c.Key, query) || strings.EqualFold(c.Label, recipient) ||
			strings.EqualFold(strings.TrimSpace(c.Mention), "@"+query)
		for _, field := range strings.Fields(c.Match) {
			exact = exact || strings.TrimLeft(field, "@~") == want
		}
		if exact {
			found = append(found, c)
		}
	}
	if len(found) > 1 {
		return backend.UserCandidate{}, false, fmt.Errorf("%s matches more than one user", recipient)
	}
	if len(found) == 0 {
		return backend.UserCandidate{}, false, nil
	}
	return found[0], true, nil
}

// messageLine is one message as JSON Lines prints it.
type messageLine struct {
	Time      time.Time `json:"time"`
	Backend   string    `json:"backend"`
	Account   string    `json:"account"`
	Chat      string    `json:"chat"`
	ChatTitle string    `json:"chat_title"`
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	SenderID  string    `json:"sender_id"`
	ReplyTo   string    `json:"reply_to"`
	Text      string    `json:"text"`
	Media     string    `json:"media"`
}

func newMessageLine(a *account.Account, e backend.ChatEntry, msg backend.Message) messageLine {
	kind, title := chatKind(e)
	return messageLine{
		Time: msg.Timestamp.UTC(), Backend: a.BackendType, Account: a.Handle(),
		Chat: e.ID.Id, ChatTitle: title, Kind: kind, ID: msg.ID,
		Sender: backend.GenericMessageSender(msg), SenderID: msg.SenderID,
		ReplyTo: msg.ReplyToID, Text: msg.Text, Media: msg.MediaLabel,
	}
}

func cmdAccounts(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: wiregram accounts")
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	out := json.NewEncoder(os.Stdout)
	for _, a := range s.mgr.List() {
		status := "ok"
		if a.Snapshot().NeedsLogin || a.NeverAuthenticated() {
			status = "needs-login"
		}
		line := struct {
			Backend string `json:"backend"`
			Account string `json:"account"`
			Label   string `json:"label"`
			Status  string `json:"status"`
		}{a.BackendType, a.Handle(), textfmt.Plain(a.Label()), status}
		if err := out.Encode(line); err != nil {
			return fmt.Errorf("write accounts: %w", err)
		}
	}
	return nil
}

func cmdChats(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: wiregram chats BACKEND ACCOUNT")
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	a, err := findAccount(s.mgr, args[0], args[1])
	if err != nil {
		return err
	}
	b, err := connect(ctx, s.mgr, a)
	if err != nil {
		return err
	}
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return fmt.Errorf("load chats: %w", err)
	}
	out := json.NewEncoder(os.Stdout)
	for _, e := range entries {
		kind, title := chatKind(e)
		line := struct {
			Chat   string `json:"chat"`
			Title  string `json:"title"`
			Kind   string `json:"kind"`
			Unread int    `json:"unread"`
		}{e.ID.Id, title, kind, e.Unread}
		if err := out.Encode(line); err != nil {
			return fmt.Errorf("write chats: %w", err)
		}
	}
	return nil
}

func cmdSend(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reply := flags.String("reply", "", "")
	usage := fmt.Errorf("usage: wiregram send [-reply ID] BACKEND ACCOUNT RECIPIENT [MESSAGE...]")
	if err := flags.Parse(args); err != nil || flags.NArg() < 3 {
		return usage
	}
	text := strings.Join(flags.Args()[3:], " ")
	if flags.NArg() == 3 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("give the message after RECIPIENT, or pipe it on stdin")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}
		text = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the message is empty")
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	a, err := findAccount(s.mgr, flags.Arg(0), flags.Arg(1))
	if err != nil {
		return err
	}
	b, err := connect(ctx, s.mgr, a)
	if err != nil {
		return err
	}
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return fmt.Errorf("load chats: %w", err)
	}
	ch, err := findChat(ctx, b, entries, flags.Arg(2))
	if err != nil {
		return err
	}
	msg, err := b.Send(ctx, ch, text, *reply)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(newMessageLine(a, ch, msg)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// streamed is one account being streamed: its subscription on the backend it
// had when subscribed, and the status last reported on stderr.
type streamed struct {
	backend backend.Backend
	sub     *backend.Subscription
	status  string
}

func cmdStream(ctx context.Context, args []string) error {
	if len(args) > 3 {
		return fmt.Errorf("usage: wiregram stream [BACKEND [ACCOUNT [RECIPIENT]]]")
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	var selected []*account.Account
	switch len(args) {
	case 0:
		for _, a := range s.mgr.List() {
			if a.BackendType != "whatsapp" && !a.Snapshot().NeedsLogin && !a.NeverAuthenticated() {
				selected = append(selected, a)
			}
		}
	case 1:
		typ, err := backendType(args[0])
		if err != nil {
			return err
		}
		for _, a := range s.mgr.List() {
			if a.BackendType == typ && !a.Snapshot().NeedsLogin && !a.NeverAuthenticated() {
				selected = append(selected, a)
			}
		}
	default:
		a, err := findAccount(s.mgr, args[0], args[1])
		if err != nil {
			return err
		}
		selected = []*account.Account{a}
	}
	if len(selected) == 0 {
		return fmt.Errorf("no signed-in accounts to stream")
	}

	var filter backend.Filter
	filter.Kinds = backend.EventMessage
	if len(args) == 3 {
		b, err := connect(ctx, s.mgr, selected[0])
		if err != nil {
			return err
		}
		entries, _, err := b.Dialogs(ctx, false)
		if err != nil {
			return fmt.Errorf("load chats: %w", err)
		}
		ch, err := findChat(ctx, b, entries, args[2])
		if err != nil {
			return err
		}
		if ch.ID.Backend == "email" && ch.IsDM {
			return fmt.Errorf("stream email by folder; wiregram chats lists them")
		}
		filter.Chat = ch.ID
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan messageLine)
	writeErr := make(chan error, 1)
	go func() {
		out := json.NewEncoder(os.Stdout)
		for line := range lines {
			if err := out.Encode(line); err != nil {
				writeErr <- err
				cancel()
				return
			}
		}
	}()

	for _, a := range selected {
		s.mgr.Start(ctx, a, nil)
	}
	subs := make(map[*account.Account]*streamed)
	defer func() {
		for _, st := range subs {
			if st.sub != nil {
				st.sub.Close()
			}
		}
	}()
	for {
		changed := s.mgr.Changes()
		for _, a := range selected {
			syncStream(ctx, a, subs, filter, lines)
		}
		select {
		case <-ctx.Done():
			select {
			case err := <-writeErr:
				if errors.Is(err, syscall.EPIPE) {
					return nil
				}
				return fmt.Errorf("write message: %w", err)
			default:
			}
			return nil
		case <-changed:
		}
	}
}

// syncStream keeps one subscription on a while it is online, renewed when it
// reconnects with a new backend, and reports its status changes on stderr.
func syncStream(ctx context.Context, a *account.Account, subs map[*account.Account]*streamed,
	filter backend.Filter, lines chan<- messageLine) {
	st := subs[a]
	if st == nil {
		st = &streamed{}
		subs[a] = st
	}
	s := a.Snapshot()
	online := s.Status == "online" && s.Backend != nil
	if s.Status != st.status {
		if st.status != "" || !online {
			msg := s.Status
			if s.Notice != "" {
				msg += ": " + s.Notice
			}
			fmt.Fprintf(os.Stderr, "%s %s %s\n", a.BackendType, a.Handle(), msg)
		}
		st.status = s.Status
	}
	if online && st.backend == s.Backend {
		return
	}
	if st.sub != nil {
		st.sub.Close()
		st.backend, st.sub = nil, nil
	}
	if !online {
		return
	}
	st.backend, st.sub = s.Backend, s.Backend.Events().Subscribe(filter)
	go pumpStream(ctx, a, s.Backend, st.sub, lines)
}

// pumpStream turns a subscription's incoming messages into lines. Chat titles
// come from the chat list, reloaded when a message arrives in a chat not on it.
func pumpStream(ctx context.Context, a *account.Account, b backend.Backend,
	sub *backend.Subscription, lines chan<- messageLine) {
	chats := make(map[backend.ChatID]backend.ChatEntry)
	load := func() {
		entries, _, err := b.Dialogs(ctx, false)
		if err != nil {
			return
		}
		for _, e := range entries {
			chats[e.ID] = e
		}
	}
	load()
	for ev := range sub.C {
		if ev.Message == nil || ev.Message.Outgoing {
			continue
		}
		e, ok := chats[ev.Chat]
		if !ok {
			load()
			if e, ok = chats[ev.Chat]; !ok {
				e = backend.ChatEntry{ID: ev.Chat, IsDM: ev.DM}
				chats[ev.Chat] = e
			}
		}
		select {
		case lines <- newMessageLine(a, e, *ev.Message):
		case <-ctx.Done():
			return
		}
	}
}

func cmdReset(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("reset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	yes := flags.Bool("y", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: wiregram reset [-y]")
	}
	if !*yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("reset needs -y when not run in a terminal")
		}
		fmt.Fprint(os.Stderr, "Reset wiregram? This signs out of every account and deletes all local data [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return nil
		}
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	accounts := s.mgr.List()
	for _, a := range accounts {
		if !a.Snapshot().NeedsLogin && !a.NeverAuthenticated() {
			s.mgr.Start(ctx, a, nil)
		}
	}
	// Give the accounts a moment online so each can log out on its server.
	settle, cancel := context.WithTimeout(ctx, 20*time.Second)
	for settle.Err() == nil {
		changed := s.mgr.Changes()
		connecting := false
		for _, a := range accounts {
			st := a.Snapshot().Status
			connecting = connecting || st == "connecting" || st == "reconnecting"
		}
		if !connecting {
			break
		}
		select {
		case <-settle.Done():
		case <-changed:
		}
	}
	cancel()
	for _, a := range accounts {
		s.mgr.Remove(ctx, a) // a failed server logout must not stop the reset
	}
	s.Close()
	if err := config.Wipe(s.dir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "wiregram has been reset")
	return nil
}

// cmdDryRun prints the reply the bot answering the chat would send to each
// given message, or to the newest incoming one, without sending anything.
func cmdDryRun(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("dry-run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var ids []string
	flags.Func("message", "", func(v string) error { ids = append(ids, v); return nil })
	if err := flags.Parse(args); err != nil || flags.NArg() != 3 {
		return fmt.Errorf("usage: wiregram dry-run [-message ID]... BACKEND ACCOUNT RECIPIENT")
	}
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	a, err := findAccount(s.mgr, flags.Arg(0), flags.Arg(1))
	if err != nil {
		return err
	}
	db, err := agent.OpenAgentDB(s.db.SQL(), s.dir)
	if err != nil {
		return err
	}
	rt := account.NewAgentRuntime(ctx, db, s.mgr)
	defer rt.Stop()
	b, err := connect(ctx, s.mgr, a)
	if err != nil {
		return err
	}
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return fmt.Errorf("load chats: %w", err)
	}
	ch, err := findChat(ctx, b, entries, flags.Arg(2))
	if err != nil {
		return err
	}
	history, err := b.History(ctx, ch)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}
	if len(ids) == 0 {
		for i := len(history) - 1; i >= 0 && len(ids) == 0; i-- {
			if !history[i].Outgoing {
				ids = append(ids, history[i].ID)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("no incoming message to answer in %s", flags.Arg(2))
		}
	}
	out := json.NewEncoder(os.Stdout)
	_, title := chatKind(ch)
	for _, id := range ids {
		i := slices.IndexFunc(history, func(m backend.Message) bool { return m.ID == id })
		if i < 0 {
			return fmt.Errorf("message %s is not among the chat's recent messages", id)
		}
		rec, reply, err := rt.DryRun(a, b, ch, history[:i+1], history[i])
		if err != nil {
			return err
		}
		line := struct {
			Agent     string `json:"agent"`
			Chat      string `json:"chat"`
			ChatTitle string `json:"chat_title"`
			ReplyTo   string `json:"reply_to"`
			Message   string `json:"message"`
			Reply     string `json:"reply"`
		}{rec.Name, ch.ID.Id, title, id, backend.GenericMessageText(history[i]), reply}
		if err := out.Encode(line); err != nil {
			return fmt.Errorf("write reply: %w", err)
		}
	}
	return nil
}
