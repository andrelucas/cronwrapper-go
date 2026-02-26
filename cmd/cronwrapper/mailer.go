package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
