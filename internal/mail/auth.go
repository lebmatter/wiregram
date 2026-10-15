package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"wiregram/internal/config"
)

const (
	authPassword = "password"
	authGoogle   = "google"
)

// Identity is what an email account saves in wiregram.db.
type Identity struct {
	Email        string `json:"email"`
	IMAPHost     string `json:"imap_host"`
	SMTPHost     string `json:"smtp_host"`
	Auth         string `json:"auth"`
	Password     string `json:"password,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// signedIn reports whether the identity holds credentials to try.
func (id Identity) signedIn() bool {
	return id.Email != "" && (id.Password != "" || id.RefreshToken != "")
}

// smtpHostFor guesses the submission host from the IMAP host.
func smtpHostFor(imapHost string) string {
	if isGmail(imapHost) {
		return "smtp.gmail.com"
	}
	if rest, ok := strings.CutPrefix(imapHost, "imap."); ok {
		return "smtp." + rest
	}
	return imapHost
}

func isGmail(host string) bool {
	return host == "imap.gmail.com" || host == "imap.googlemail.com"
}

var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

func googleConfig(redirect string) (*oauth2.Config, error) {
	id, secret, ok := config.GoogleClient()
	if !ok {
		return nil, fmt.Errorf("this build has no Google sign-in; use an app password")
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     googleEndpoint,
		RedirectURL:  redirect,
		Scopes:       []string{"https://mail.google.com/"},
	}, nil
}

// Authenticate signs in with the saved credentials, or asks for the address
// and an app password or a Google sign-in in the browser.
func (b *MailBackend) Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error {
	if b.Account.signedIn() && prompt == nil {
		// Reconnects retry in the background; a revoked password shows as a notice.
		return nil
	}
	if prompt == nil {
		return fmt.Errorf("sign in to this email account again")
	}
	answer, err := prompt("Email address: ", false)
	if err != nil {
		return fmt.Errorf("read email address: %w", err)
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(answer))
	if err != nil {
		return fmt.Errorf("that is not an email address")
	}
	b.Account.Email = strings.ToLower(addr.Address)
	b.Account.Password, b.Account.RefreshToken = "", ""
	method := authPassword
	if _, _, ok := config.GoogleClient(); ok && isGmail(b.Account.IMAPHost) {
		choice, err := prompt("Sign in with (1) an app password or (2) Google in the browser: ", false)
		if err != nil {
			return fmt.Errorf("read sign-in choice: %w", err)
		}
		if strings.TrimSpace(choice) == "2" {
			method = authGoogle
		}
	}
	b.Account.Auth = method
	if method == authGoogle {
		token, err := googleSignIn(ctx, prompt, b.Account.Email)
		if err != nil {
			return err
		}
		b.Account.RefreshToken = token.RefreshToken
		b.setToken(token)
	} else {
		ask := "App password: "
		if isGmail(b.Account.IMAPHost) {
			ask += "\nTurn on 2-Step Verification for your Google account, then create " +
				"an app password at https://myaccount.google.com/apppasswords"
		}
		pw, err := prompt(ask, true)
		if err != nil {
			return fmt.Errorf("read app password: %w", err)
		}
		// Gmail shows app passwords in groups of four.
		b.Account.Password = strings.ReplaceAll(strings.TrimSpace(pw), " ", "")
	}
	c, err := b.dial(ctx, nil)
	if err != nil {
		return err
	}
	// The labels are listed now, so Labels… has them before the first sync.
	b.connMu.Lock()
	err = b.findMailboxes(c)
	b.connMu.Unlock()
	if err != nil && report != nil {
		report(err)
	}
	c.Logout().Wait()
	c.Close()
	return nil
}

// googleSignIn runs Google's desktop sign-in: the browser is sent to a
// loopback address where this function collects the code, with PKCE.
func googleSignIn(ctx context.Context, prompt config.PromptFunc, email string) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start Google sign-in: %w", err)
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	cfg, err := googleConfig(redirect)
	if err != nil {
		ln.Close()
		return nil, err
	}
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		ln.Close()
		return nil, fmt.Errorf("start Google sign-in: %w", err)
	}
	state := hex.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()
	link := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("login_hint", email))

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("state") != state {
				http.Error(w, "unexpected sign-in response", http.StatusBadRequest)
				return
			}
			res := result{code: q.Get("code")}
			if e := q.Get("error"); e != "" || res.code == "" {
				res.err = fmt.Errorf("Google sign-in was not completed: %s", e)
			}
			select {
			case results <- res:
			default:
			}
			fmt.Fprintln(w, "Signed in. You can close this tab and return to wiregram.")
		})}
	go srv.Serve(ln)
	defer srv.Close()
	openBrowser(link)

	if _, err := prompt("Finish signing in in your browser, then press Enter. "+
		"If no browser opened, visit "+link+" : ", false); err != nil {
		return nil, fmt.Errorf("read sign-in confirmation: %w", err)
	}
	var res result
	select {
	case res = <-results:
	case <-time.After(2 * time.Minute):
		return nil, fmt.Errorf("Google sign-in timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}
	token, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("finish Google sign-in: %w", err)
	}
	if token.RefreshToken == "" {
		return nil, fmt.Errorf("Google did not grant offline access; try again")
	}
	return token, nil
}

func openBrowser(link string) {
	opener, args := "xdg-open", []string{link}
	switch runtime.GOOS {
	case "darwin":
		opener = "open"
	case "windows":
		opener, args = "rundll32", []string{"url.dll,FileProtocolHandler", link}
	}
	cmd := exec.Command(opener, args...)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func (b *MailBackend) setToken(t *oauth2.Token) {
	cfg, err := googleConfig("")
	if err != nil {
		return
	}
	b.mu.Lock()
	b.tokens = cfg.TokenSource(context.Background(), t)
	b.mu.Unlock()
}

// accessToken returns a fresh Google access token, refreshing as needed.
func (b *MailBackend) accessToken(ctx context.Context) (string, error) {
	b.mu.Lock()
	ts := b.tokens
	b.mu.Unlock()
	if ts == nil {
		b.setToken(&oauth2.Token{RefreshToken: b.Account.RefreshToken})
		b.mu.Lock()
		ts = b.tokens
		b.mu.Unlock()
	}
	if ts == nil {
		return "", fmt.Errorf("this build has no Google sign-in; use an app password")
	}
	t, err := ts.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return "", fmt.Errorf("Google sign-in expired; remove and add this account again")
		}
		return "", fmt.Errorf("refresh Google sign-in: %w", err)
	}
	return t.AccessToken, nil
}

// revokeGoogle ends the account's Google grant on logout.
func (b *MailBackend) revokeGoogle(ctx context.Context) error {
	form := url.Values{"token": {b.Account.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("revoke Google sign-in: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke Google sign-in: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("revoke Google sign-in: HTTP %d", resp.StatusCode)
	}
	return nil
}

func xoauth2Response(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
}

// xoauth2 is Google's XOAUTH2 SASL mechanism for IMAP.
type xoauth2 struct{ user, token string }

func (a *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", xoauth2Response(a.user, a.token), nil
}

// Next answers the error challenge with an empty response, as the
// mechanism requires, so the server reports the failure.
func (a *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }

// smtpXOAuth2 is the same mechanism for net/smtp.
type smtpXOAuth2 struct{ user, token string }

func (a smtpXOAuth2) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "XOAUTH2", xoauth2Response(a.user, a.token), nil
}

func (a smtpXOAuth2) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}
