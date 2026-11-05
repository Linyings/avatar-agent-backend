package campaign

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

const sendTimeout = 30 * time.Second

type SMTPMailer struct {
	cfg config.Config
}

func NewSMTPMailer(cfg config.Config) (*SMTPMailer, error) {
	if !cfg.CampaignSMTPConfigured() {
		return nil, errors.New("dedicated campaign SMTP is not configured")
	}
	return &SMTPMailer{cfg: cfg}, nil
}

func (m *SMTPMailer) Send(ctx context.Context, item Message) error {
	if strings.TrimSpace(item.To) == "" || strings.ContainsAny(item.To, "\r\n") {
		return errors.New("campaign recipient is invalid")
	}
	if strings.TrimSpace(item.Subject) == "" || strings.ContainsAny(item.Subject, "\r\n") {
		return errors.New("campaign subject is invalid")
	}
	if strings.TrimSpace(item.UnsubscribeURL) == "" || !strings.HasPrefix(item.UnsubscribeURL, "https://") {
		return errors.New("campaign message requires an https unsubscribe URL")
	}

	from := formatAddress(m.cfg.CampaignSMTPFromName, m.cfg.CampaignSMTPFromEmail)
	replyTo := strings.TrimSpace(m.cfg.CampaignSMTPReplyTo)
	if replyTo == "" {
		replyTo = m.cfg.CampaignSMTPFromEmail
	}
	message := strings.Join([]string{
		"From: " + from,
		"To: " + item.To,
		"Reply-To: " + replyTo,
		"Subject: " + mime.QEncoding.Encode("UTF-8", item.Subject),
		"Message-ID: " + item.MessageID,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"List-Unsubscribe: <" + item.UnsubscribeURL + ">",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"Auto-Submitted: auto-generated",
		"X-Auto-Response-Suppress: All",
		"",
		item.Text,
	}, "\r\n")

	auth := smtp.PlainAuth("", m.cfg.CampaignSMTPUsername, m.cfg.CampaignSMTPPassword, m.cfg.CampaignSMTPHost)
	hostPort := fmt.Sprintf("%s:%d", m.cfg.CampaignSMTPHost, m.cfg.CampaignSMTPPort)
	done := make(chan error, 1)
	go func() {
		if m.cfg.CampaignSMTPUseTLS && m.cfg.CampaignSMTPPort == 465 {
			done <- sendImplicitTLS(hostPort, m.cfg.CampaignSMTPHost, auth, m.cfg.CampaignSMTPFromEmail, item.To, []byte(message))
			return
		}
		done <- sendStartTLS(hostPort, m.cfg.CampaignSMTPHost, auth, m.cfg.CampaignSMTPFromEmail, item.To, []byte(message), m.cfg.CampaignSMTPUseTLS)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func sendImplicitTLS(addr, host string, auth smtp.Auth, from, to string, message []byte) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: sendTimeout}, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	return send(client, auth, from, to, message)
}

func sendStartTLS(addr, host string, auth smtp.Auth, from, to string, message []byte, requireTLS bool) error {
	conn, err := net.DialTimeout("tcp", addr, sendTimeout)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = client.Close()
			return err
		}
	} else if requireTLS {
		_ = client.Close()
		return errors.New("campaign SMTP server does not support STARTTLS")
	}
	return send(client, auth, from, to, message)
}

func send(client *smtp.Client, auth smtp.Auth, from, to string, message []byte) error {
	defer client.Close()
	if err := client.Auth(auth); err != nil {
		return err
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func formatAddress(name, email string) string {
	if strings.TrimSpace(name) == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("UTF-8", name), email)
}
