package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"wiregram/internal/config"
)

type authenticator struct{ prompt config.PromptFunc }

func (a authenticator) Phone(context.Context) (string, error) {
	phone, err := a.prompt("Phone (international format): ", false)
	return strings.TrimSpace(phone), err
}
func (a authenticator) Code(context.Context, *tg.AuthSentCode) (string, error) {
	code, err := a.prompt("Code: ", false)
	return strings.TrimSpace(code), err
}
func (a authenticator) Password(context.Context) (string, error) {
	return a.prompt("2FA Password: ", true)
}
func (a authenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return fmt.Errorf("create a Telegram account in an official app before signing in")
}
func (a authenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("create a Telegram account in an official app before signing in")
}

// Keep the code hash when retrying, rather than requesting a new OTP each time.
type retryAuth struct {
	*auth.Client
	user   authenticator
	report func(error)
}

func (a retryAuth) SignIn(ctx context.Context, phone, code, hash string) (*tg.AuthAuthorization, error) {
	for {
		result, err := a.Client.SignIn(ctx, phone, code, hash)
		if !tgerr.Is(err, "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY") {
			return result, err
		}
		a.report(fmt.Errorf("The code is incorrect. Please try again."))
		code, err = a.user.Code(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("read code: %w", err)
		}
	}
}

func (a retryAuth) Password(ctx context.Context, password string) (*tg.AuthAuthorization, error) {
	for {
		result, err := a.Client.Password(ctx, password)
		if !errors.Is(err, auth.ErrPasswordInvalid) {
			return result, err
		}
		a.report(fmt.Errorf("The password is incorrect. Please try again."))
		password, err = a.user.Password(ctx)
		if err != nil {
			return nil, fmt.Errorf("read password: %w", err)
		}
	}
}

func (c *Client) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	status, err := c.Telegram.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("check authentication: %w", err)
	}
	if status.Authorized {
		return nil
	}
	user := authenticator{prompt: prompt}
	flow := auth.NewFlow(user, auth.SendCodeOptions{})
	for {
		err := flow.Run(ctx, retryAuth{Client: c.Telegram.Auth(), user: user, report: report})
		if tgerr.Is(err, "PHONE_CODE_EXPIRED", "PHONE_NUMBER_INVALID") {
			report(fmt.Errorf("The phone number is invalid or the code expired. Please sign in again."))
			continue
		}
		if err != nil {
			return fmt.Errorf("sign in: %w", err)
		}
		return nil
	}
}
