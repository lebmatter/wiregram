package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// draft is one mail about to be sent.
type draft struct {
	To         string
	Cc         string
	Subject    string
	Body       string
	InReplyTo  string
	References string
	MessageID  string
	Date       time.Time
	Attach     []attachment
}

// attachment is a file sent with a mail.
type attachment struct {
	Name string
	Type string
	Data []byte
}

// maxAttach caps what one mail carries, the limit most servers accept.
const maxAttach = 25 << 20

// compose works out the mail the CLI's send writes to an address. A first
// line starting with "Subject:" starts a new conversation; otherwise it
// replies to parent, and without one the first line becomes the subject.
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
	d.Subject = withPrefix(parent.Subject, "Re:")
	d.Body = text
	d.threadUnder(*parent)
	return d
}

// threadUnder makes d a reply to parent in the headers clients thread by.
func (d *draft) threadUnder(parent row) {
	if strings.HasSuffix(parent.MessageID, "@wiregram") {
		return // a name of wiregram's own, not the mail's
	}
	d.InReplyTo = parent.MessageID
	d.References = strings.TrimSpace(angle(parent.InReplyTo) + " " + angle(parent.MessageID))
}

// withPrefix puts prefix ("Re:" or "Fwd:") before subject unless it is there.
func withPrefix(subject, prefix string) string {
	if _, ok := cutPrefixFold(subject, prefix); ok {
		return subject
	}
	return prefix + " " + subject
}

// recipients parses comma-separated addresses, as the composer's To and Cc
// fields hold them, into the bare addresses and the list as a header, its
// names encoded.
func recipients(list string) (addrs []string, header string, err error) {
	if strings.TrimSpace(list) == "" {
		return nil, "", nil
	}
	parsed, err := mail.ParseAddressList(list)
	if err != nil {
		return nil, "", fmt.Errorf("read the address list %q: %w", list, err)
	}
	var names []string
	for _, a := range parsed {
		addrs, names = append(addrs, a.Address), append(names, a.String())
	}
	return addrs, strings.Join(names, ", "), nil
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

// encode renders d as an RFC 5322 message: its text in UTF-8
// quoted-printable, and with attachments, multipart/mixed.
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
	header("Cc", d.Cc)
	header("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	header("Date", d.Date.Format(time.RFC1123Z))
	header("Message-ID", angle(d.MessageID))
	header("In-Reply-To", angle(d.InReplyTo))
	header("References", d.References)
	header("MIME-Version", "1.0")
	if len(d.Attach) == 0 {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeText(&buf, d.Body); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	mw := multipart.NewWriter(&buf)
	header("Content-Type", mime.FormatMediaType("multipart/mixed",
		map[string]string{"boundary": mw.Boundary()}))
	buf.WriteString("\r\n")
	text, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, fmt.Errorf("encode mail: %w", err)
	}
	if err := writeText(text, d.Body); err != nil {
		return nil, err
	}
	for _, a := range d.Attach {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type": {mime.FormatMediaType(a.Type, map[string]string{"name": a.Name})},
			"Content-Disposition": {mime.FormatMediaType("attachment",
				map[string]string{"filename": a.Name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, fmt.Errorf("encode attachment %s: %w", a.Name, err)
		}
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			fmt.Fprintf(w, "%s\r\n", enc[:76])
			enc = enc[76:]
		}
		fmt.Fprintf(w, "%s\r\n", enc)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("encode mail: %w", err)
	}
	return buf.Bytes(), nil
}

// writeText writes body as quoted-printable with CRLF line breaks.
func writeText(w io.Writer, body string) error {
	qp := quotedprintable.NewWriter(w)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return fmt.Errorf("encode mail: %w", err)
	}
	if err := qp.Close(); err != nil {
		return fmt.Errorf("encode mail: %w", err)
	}
	return nil
}

// submit sends an encoded mail to every recipient over implicit TLS on
// port 465.
func (b *MailBackend) submit(ctx context.Context, rcpts []string, data []byte) error {
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
	for _, to := range rcpts {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("send mail to %s: %w", to, err)
		}
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
	// The server has accepted the mail; failing to say goodbye must not make
	// it look unsent and be sent again.
	_ = c.Quit()
	return nil
}
