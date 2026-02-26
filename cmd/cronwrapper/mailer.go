package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	sasl "github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

// Mailer sends a message body to a recipient with a given subject.
type Mailer interface {
	Send(ctx context.Context, to string, subject string, body io.Reader) error
}

// MailxMailer uses the local mailx binary for message delivery.
type MailxMailer struct {
	Path string
}

func (m MailxMailer) Send(ctx context.Context, to string, subject string, body io.Reader) error {
	path := m.Path
	if path == "" {
		path = "mailx"
	}

	cmd := exec.CommandContext(ctx, path, "-s", subject, to)
	cmd.Stdin = body
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("send mail via %s: %w", path, err)
	}
	return nil
}

type SMTPSecurityMode string

const (
	SMTPSecurityNone     SMTPSecurityMode = "none"
	SMTPSecurityStartTLS SMTPSecurityMode = "starttls"
	SMTPSecurityTLS      SMTPSecurityMode = "tls"
)

type SMTPConfig struct {
	Addr               string
	Security           SMTPSecurityMode
	ServerName         string
	InsecureSkipVerify bool
	CACertFile         string
	ClientCertFile     string
	ClientKeyFile      string
	Username           string
	Password           string
	From               string
}

// SMTPMailer delivers mail directly through an SMTP server.
type SMTPMailer struct {
	Config SMTPConfig
}

func (m SMTPMailer) Send(ctx context.Context, to string, subject string, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if to == "" {
		return fmt.Errorf("missing recipient")
	}
	if m.Config.From == "" {
		return fmt.Errorf("missing sender")
	}

	client, err := m.dial()
	if err != nil {
		return err
	}
	defer client.Close()

	if m.Config.Username != "" {
		auth := sasl.NewPlainClient("", m.Config.Username, m.Config.Password)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(m.Config.From, nil); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to, nil); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}

	headers := smtpHeaders(m.Config.From, to, subject)
	msg := io.MultiReader(strings.NewReader(headers), body)
	if _, err := io.Copy(w, msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp finalize body: %w", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp QUIT: %w", err)
	}
	return nil
}

func (m SMTPMailer) dial() (*smtp.Client, error) {
	security := SMTPSecurityMode(strings.ToLower(string(m.Config.Security)))
	switch security {
	case "", SMTPSecurityNone:
		if m.Config.ServerName != "" || m.Config.CACertFile != "" || m.Config.ClientCertFile != "" || m.Config.ClientKeyFile != "" || m.Config.InsecureSkipVerify {
			return nil, fmt.Errorf("TLS/certificate flags require -smtp-security starttls or tls")
		}
		c, err := smtp.Dial(m.Config.Addr)
		if err != nil {
			return nil, fmt.Errorf("dial smtp: %w", err)
		}
		return c, nil
	case SMTPSecurityStartTLS:
		tlsCfg, err := m.tlsConfig()
		if err != nil {
			return nil, err
		}
		c, err := smtp.DialStartTLS(m.Config.Addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("dial smtp starttls: %w", err)
		}
		return c, nil
	case SMTPSecurityTLS:
		tlsCfg, err := m.tlsConfig()
		if err != nil {
			return nil, err
		}
		c, err := smtp.DialTLS(m.Config.Addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("dial smtp tls: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("invalid smtp security mode %q (expected none, starttls, tls)", m.Config.Security)
	}
}

func (m SMTPMailer) tlsConfig() (*tls.Config, error) {
	host, _, err := net.SplitHostPort(m.Config.Addr)
	if err != nil {
		return nil, fmt.Errorf("invalid -smtp-addr %q: %w", m.Config.Addr, err)
	}

	serverName := m.Config.ServerName
	if serverName == "" {
		serverName = host
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: m.Config.InsecureSkipVerify,
	}

	if m.Config.CACertFile != "" {
		pemData, err := os.ReadFile(m.Config.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("read -smtp-ca-cert: %w", err)
		}

		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if ok := pool.AppendCertsFromPEM(pemData); !ok {
			return nil, fmt.Errorf("parse -smtp-ca-cert: no certificates found")
		}
		cfg.RootCAs = pool
	}

	hasClientCert := m.Config.ClientCertFile != ""
	hasClientKey := m.Config.ClientKeyFile != ""
	if hasClientCert != hasClientKey {
		return nil, fmt.Errorf("-smtp-client-cert and -smtp-client-key must be provided together")
	}
	if hasClientCert {
		cert, err := tls.LoadX509KeyPair(m.Config.ClientCertFile, m.Config.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

func smtpHeaders(from, to, subject string) string {
	clean := func(s string) string {
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		return strings.TrimSpace(s)
	}

	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		clean(from),
		clean(to),
		clean(subject),
		time.Now().Format(time.RFC1123Z),
	)
}
