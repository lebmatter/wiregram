package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// draft is one mail about to be sent.
type draft struct {
	To         string
	Subject    string
	Body       string
	InReplyTo  string
	References string
	MessageID  string
	Date       time.Time
}

// compose works out the mail a chat line sends. A first line starting with
// "Subject:" starts a new conversation; otherwise it replies to parent, and
// without one the first line becomes the subject.
func compose(to, text string, parent *row) draft {
	d := draft{To: to, Date: time.Now()}
	first, rest, _ := strings.Cut(text, "\n")
	if subj, ok := cutPrefixFold(first, "subject:"); ok {
		d.Subject, d.Body = strings.TrimSpace(subj), strings.TrimLeft(rest, "\n")
		return d
	}
	if parent == nil {
		d.Subject, d.Body = strings.TrimSpace(first), strings.TrimLeft(rest, "\n")
		return d
	}
	d.Subject = parent.Subject
	if _, ok := cutPrefixFold(d.Subject, "re:"); !ok {
		d.Subject = "Re: " + d.Subject
	}
	d.Body = text
	if parent.MessageID != "" {
		d.InReplyTo = parent.MessageID
		d.References = strings.TrimSpace(angle(parent.InReplyTo) + " " + angle(parent.MessageID))
	}
	return d
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

func angle(id string) string {
	if id == "" {
		return ""
	}
	return "<" + id + ">"
}

func newMessageID(from string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("make message ID: %w", err)
	}
	domain := "wiregram.local"
	if _, host, ok := strings.Cut(from, "@"); ok {
		domain = host
	}
	return hex.EncodeToString(buf) + "@" + domain, nil
}

// encode renders d as an RFC 5322 message in UTF-8 quoted-printable.
func (d draft) encode(from string) ([]byte, error) {
	var buf bytes.Buffer
	header := func(k, v string) {
		// Values come from received mail; a line break would inject headers.
		v = strings.NewReplacer("\r", "", "\n", " ").Replace(v)
		if v != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	header("From", from)
	header("To", d.To)
	header("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	header("Date", d.Date.Format(time.RFC1123Z))
	header("Message-ID", angle(d.MessageID))
	header("In-Reply-To", angle(d.InReplyTo))
	header("References", d.References)
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	buf.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&buf)
	body := strings.ReplaceAll(strings.ReplaceAll(d.Body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, fmt.Errorf("encode mail: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("encode mail: %w", err)
	}
	return buf.Bytes(), nil
}

// submit sends an encoded mail over implicit TLS on port 465.
func (b *MailBackend) submit(ctx context.Context, to string, data []byte) error {
	host := b.Account.SMTPHost
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second},
		Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "465"))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", host, err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("connect to %s: %w", host, err)
	}
	defer c.Close()
	var auth smtp.Auth = smtp.PlainAuth("", b.Account.Email, b.Account.Password, host)
	if b.Account.Auth == authGoogle {
		token, err := b.accessToken(ctx)
		if err != nil {
			return err
		}
		auth = smtpXOAuth2{user: b.Account.Email, token: token}
	}
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("sign in to %s: %w", host, err)
	}
	if err := c.Mail(b.Account.Email); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("send mail to %s: %w", to, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return c.Quit()
}
