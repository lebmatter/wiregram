package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"wiregram/internal/backend"
)

// Bot accounts: a wiregram bot registered on the server is a Mattermost bot
// the user owns, signed in with a bot access token of its own.

type mmBot struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	OwnerID     string `json:"owner_id"`
}

type mmToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// mmServerError is a refusal from the server, carrying its own message.
type mmServerError struct {
	Status  int
	ID      string
	Message string
}

func (e *mmServerError) Error() string { return e.Message }

// botAPI is apiJSON for bot management: a refusal comes back as an
// *mmServerError so callers can explain it.
func (mb *MmBackend) botAPI(ctx context.Context, method, path string, body, result any) error {
	resp, err := mb.api(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("reach Mattermost: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var e mmAPIError
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			return &mmServerError{Status: resp.StatusCode, ID: e.ID, Message: e.Message}
		}
		return &mmServerError{Status: resp.StatusCode,
			Message: fmt.Sprintf("Mattermost refused the request (%d)", resp.StatusCode)}
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("read Mattermost reply: %w", err)
		}
	}
	return nil
}

// botError turns a refusal into one plain line.
func botError(err error, username string) error {
	e, ok := err.(*mmServerError)
	switch {
	case !ok:
		return err
	case strings.Contains(e.ID, "username_exists") || strings.Contains(e.ID, "username.exists"):
		return fmt.Errorf("@%s is taken on this server; change the handle", username)
	case strings.Contains(e.ID, "disabled") || e.Status == http.StatusNotImplemented:
		return fmt.Errorf("this server does not allow bot accounts: %s", e.Message)
	case e.Status == http.StatusForbidden:
		return fmt.Errorf("your account may not manage bots on this server: %s", e.Message)
	}
	return fmt.Errorf("Mattermost refused: %s", e.Message)
}

func (mb *MmBackend) RegisterBot(ctx context.Context, username, displayName string) (backend.BotAccount, error) {
	var bot mmBot
	err := mb.botAPI(ctx, "POST", "/bots", map[string]string{
		"username": username, "display_name": displayName, "description": "A wiregram bot",
	}, &bot)
	if err != nil {
		own, ok := mb.ownBot(ctx, username)
		if !ok {
			return backend.BotAccount{}, botError(err, username)
		}
		// One of the user's own bots, unregistered before: take it back.
		if err := mb.botAPI(ctx, "POST", "/bots/"+own.UserID+"/enable", nil, nil); err != nil {
			return backend.BotAccount{}, botError(err, username)
		}
		bot = own
		if err := mb.botAPI(ctx, "PUT", "/bots/"+bot.UserID,
			map[string]string{"display_name": displayName}, nil); err != nil {
			return backend.BotAccount{}, botError(err, username)
		}
	}
	acct := backend.BotAccount{UserID: bot.UserID, Username: bot.Username}
	if acct, err = mb.issueToken(ctx, acct); err != nil {
		mb.botAPI(ctx, "POST", "/bots/"+bot.UserID+"/disable", nil, nil)
		return acct, err
	}
	return acct, nil
}

// ownBot finds a bot named username that the user owns, enabled or not.
func (mb *MmBackend) ownBot(ctx context.Context, username string) (mmBot, bool) {
	var u mmUser
	if mb.botAPI(ctx, "GET", "/users/username/"+username, nil, &u) != nil {
		return mmBot{}, false
	}
	var bot mmBot
	if mb.botAPI(ctx, "GET", "/bots/"+u.ID+"?include_deleted=true", nil, &bot) != nil {
		return mmBot{}, false
	}
	return bot, bot.OwnerID == mb.UserID
}

func (mb *MmBackend) issueToken(ctx context.Context, bot backend.BotAccount) (backend.BotAccount, error) {
	var tok mmToken
	if err := mb.botAPI(ctx, "POST", "/users/"+bot.UserID+"/tokens",
		map[string]string{"description": "wiregram"}, &tok); err != nil {
		return bot, fmt.Errorf("issue the bot a token: %w", botError(err, bot.Username))
	}
	bot.TokenID, bot.Token = tok.ID, tok.Token
	return bot, nil
}

func (mb *MmBackend) RenameBot(ctx context.Context, bot backend.BotAccount, username, displayName string) error {
	if err := mb.botAPI(ctx, "PUT", "/bots/"+bot.UserID, map[string]string{
		"username": username, "display_name": displayName,
	}, nil); err != nil {
		return botError(err, username)
	}
	return nil
}

func (mb *MmBackend) RotateBotToken(ctx context.Context, bot backend.BotAccount) (backend.BotAccount, error) {
	old := bot.TokenID
	bot, err := mb.issueToken(ctx, bot)
	if err != nil {
		return bot, err
	}
	if err := mb.botAPI(ctx, "POST", "/users/tokens/revoke",
		map[string]string{"token_id": old}, nil); err != nil {
		return bot, fmt.Errorf("revoke the old token: %w", botError(err, bot.Username))
	}
	return bot, nil
}

func (mb *MmBackend) UnregisterBot(ctx context.Context, bot backend.BotAccount) error {
	// The token goes first, so a bot left enabled by a failure cannot sign in.
	if err := mb.botAPI(ctx, "POST", "/users/tokens/revoke",
		map[string]string{"token_id": bot.TokenID}, nil); err != nil {
		return fmt.Errorf("revoke the bot's token: %w", botError(err, bot.Username))
	}
	if err := mb.botAPI(ctx, "POST", "/bots/"+bot.UserID+"/disable", nil, nil); err != nil {
		return fmt.Errorf("disable the bot: %w", botError(err, bot.Username))
	}
	return nil
}

func (mb *MmBackend) ConnectBot(ctx context.Context, bot backend.BotAccount) (backend.Backend, error) {
	b := NewMMBackend(mb.ServerURL)
	b.Token = bot.Token
	if err := b.Authenticate(ctx, nil, nil); err != nil {
		return nil, fmt.Errorf("@%s could not sign in; rotate its token", bot.Username)
	}
	return b, nil
}
