package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

type options struct {
	to              string
	noshell         bool
	mailerTest      bool
	timestamp       bool
	notimestamp     bool
	subject         string
	subjectTemplate string
	outputPath      string
	keepOutput      bool
	mailer          string
	mailxPath       string
	smtpAddr        string
	smtpSecurity    string
	smtpServerName  string
	smtpInsecureTLS bool
	smtpCACert      string
	smtpClientCert  string
	smtpClientKey   string
	smtpUsername    string
	smtpPassword    string
	smtpPasswordEnv string
	from            string
	debug           bool
}

type subjectData struct {
	Result  string
	Host    string
	Command string
	Code    int
}

type smtpResolvedConfig struct {
	From           string
	Username       string
	Password       string
	UsernameSource string
	PasswordSource string
}

func parseFlags(args []string) (options, []string, error) {
	opt := options{
		timestamp:       true,
		subjectTemplate: "{{.Result}}: {{.Host}}: {{.Command}}",
	}

	fs := flag.NewFlagSet("cronwrapper", flag.ContinueOnError)
	var parseErr bytes.Buffer
	fs.SetOutput(&parseErr)

	fs.StringVar(&opt.to, "to", "", "email recipient (default: $LOGNAME)")
	fs.BoolVar(&opt.noshell, "noshell", false, "run command directly without a shell")
	fs.BoolVar(&opt.mailerTest, "mailer-test", false, "send a test message using the selected mailer and exit without running the command")
	fs.BoolVar(&opt.timestamp, "timestamp", true, "add start/end timestamps in header")
	fs.BoolVar(&opt.notimestamp, "notimestamp", false, "disable timestamps in header")
	fs.BoolVar(&opt.debug, "debug", false, "enable debug logging")
	fs.BoolVar(&opt.keepOutput, "keep-output", false, "keep output file when using temp capture")
	fs.StringVar(&opt.subject, "subject", "", "explicit email subject (overrides -subject-template)")
	fs.StringVar(&opt.subjectTemplate, "subject-template", opt.subjectTemplate, "Go template for subject: {{.Result}} {{.Host}} {{.Command}} {{.Code}}")
	fs.StringVar(&opt.outputPath, "output", "", "capture file path (default: temp file)")
	fs.StringVar(&opt.mailer, "mailer", "mailx", "mail backend: mailx or smtp")
	fs.StringVar(&opt.mailxPath, "mailx-path", "mailx", "mailx executable path")
	fs.StringVar(&opt.smtpAddr, "smtp-addr", "127.0.0.1:25", "SMTP server address in host:port form")
	fs.StringVar(&opt.smtpSecurity, "smtp-security", "starttls", "SMTP transport security: none, starttls, or tls")
	fs.StringVar(&opt.smtpServerName, "smtp-server-name", "", "TLS server name override (default: host from -smtp-addr)")
	fs.BoolVar(&opt.smtpInsecureTLS, "smtp-insecure-skip-verify", false, "skip TLS certificate verification (discouraged)")
	fs.StringVar(&opt.smtpCACert, "smtp-ca-cert", "", "PEM file containing additional trusted CA certificates")
	fs.StringVar(&opt.smtpClientCert, "smtp-client-cert", "", "client certificate PEM file for mutual TLS")
	fs.StringVar(&opt.smtpClientKey, "smtp-client-key", "", "client private key PEM file for mutual TLS")
	fs.StringVar(&opt.smtpUsername, "smtp-username", "", "SMTP SASL username")
	fs.StringVar(&opt.smtpPassword, "smtp-password", "", "SMTP SASL password (discouraged: prefer -smtp-password-env)")
	fs.StringVar(&opt.smtpPasswordEnv, "smtp-password-env", "CRONWRAPPER_SMTP_PASSWORD", "env var containing SMTP SASL password")
	fs.StringVar(&opt.from, "from", "", "SMTP envelope/header sender (default: $LOGNAME@hostname)")

	if err := fs.Parse(args); err != nil {
		return options{}, nil, fmt.Errorf("%v\n%s", err, strings.TrimSpace(parseErr.String()))
	}
	if opt.notimestamp {
		opt.timestamp = false
	}
	return opt, fs.Args(), nil
}

func shortHostname() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown-host"
	}
	if i := strings.IndexByte(host, '.'); i >= 0 {
		return host[:i]
	}
	return host
}

func renderSubject(opt options, data subjectData) (string, error) {
	if opt.subject != "" {
		return opt.subject, nil
	}
	t, err := template.New("subject").Parse(opt.subjectTemplate)
	if err != nil {
		return "", fmt.Errorf("parse subject template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render subject template: %w", err)
	}
	return b.String(), nil
}

func runCommand(ctx context.Context, cmdArgs []string, useShell bool, out io.Writer) (int, error) {
	if len(cmdArgs) == 0 {
		return 0, fmt.Errorf("missing command")
	}

	var cmd *exec.Cmd
	if useShell {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(cmdArgs, " "))
	} else {
		cmd = exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func headerContent(addTimestamp bool, start, end time.Time, exitCode int) string {
	var b strings.Builder
	hasHeader := false
	if addTimestamp {
		hasHeader = true
		b.WriteString("Start time: ")
		b.WriteString(start.Format(time.ANSIC))
		b.WriteByte('\n')
		b.WriteString("End time: ")
		b.WriteString(end.Format(time.ANSIC))
		b.WriteByte('\n')
	}
	if exitCode != 0 {
		hasHeader = true
		b.WriteString(fmt.Sprintf("Shell return code: %d\n", exitCode))
	}
	if hasHeader {
		b.WriteString("-- HEADER ENDS --\n\n")
	}
	return b.String()
}

func main() {
	opt, cmdArgs, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "usage: cronwrapper [flags] command [args...]")
		os.Exit(2)
	}
	if len(cmdArgs) == 0 && !opt.mailerTest {
		fmt.Fprintln(os.Stderr, "error: command is required")
		fmt.Fprintln(os.Stderr, "usage: cronwrapper [flags] command [args...]")
		os.Exit(2)
	}

	rcpt := opt.to
	if rcpt == "" {
		rcpt = os.Getenv("LOGNAME")
	}
	if rcpt == "" {
		fmt.Fprintln(os.Stderr, "error: recipient not set; use -to or set LOGNAME")
		os.Exit(2)
	}
	mailer, err := newMailer(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if opt.mailerTest {
		if len(cmdArgs) > 0 {
			fmt.Fprintln(os.Stderr, "warning: -mailer-test is set; command arguments are ignored and will not be executed")
		}
		subj := opt.subject
		if subj == "" {
			subj = fmt.Sprintf("TEST: %s: mailer=%s", shortHostname(), strings.ToLower(opt.mailer))
		}
		body := strings.NewReader(mailerTestBody(opt))
		if err := mailer.Send(context.Background(), rcpt, subj, body); err != nil {
			fmt.Fprintln(os.Stderr, "mailer test failed:", err)
			os.Exit(1)
		}
		if opt.debug {
			fmt.Fprintln(os.Stderr, "debug: mailer test message sent")
		}
		return
	}

	capturePath := opt.outputPath
	isTempCapture := false
	if capturePath == "" {
		capturePath = filepath.Join(os.TempDir(), fmt.Sprintf("cronwrapper-%d.out", time.Now().UnixNano()))
		isTempCapture = true
	}

	captureFile, err := os.OpenFile(capturePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer captureFile.Close()
	if isTempCapture && !opt.keepOutput {
		defer os.Remove(capturePath)
	}

	start := time.Now()
	exitCode, err := runCommand(context.Background(), cmdArgs, !opt.noshell, captureFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error running command:", err)
		os.Exit(1)
	}
	end := time.Now()

	if _, err := captureFile.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintln(os.Stderr, "error rewinding capture file:", err)
		os.Exit(1)
	}

	result := "SUCCESS"
	if exitCode != 0 {
		result = "FAILURE"
	}

	subj, err := renderSubject(opt, subjectData{
		Result:  result,
		Host:    shortHostname(),
		Command: strings.Join(cmdArgs, " "),
		Code:    exitCode,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error building subject:", err)
		os.Exit(1)
	}

	head := headerContent(opt.timestamp, start, end, exitCode)
	body := io.MultiReader(strings.NewReader(head), captureFile)
	if err := mailer.Send(context.Background(), rcpt, subj, body); err != nil {
		fmt.Fprintln(os.Stderr, "email command failed:", err)
		os.Exit(1)
	}

	if opt.debug {
		fmt.Fprintf(os.Stderr, "debug: command exit code=%d, output=%s\n", exitCode, capturePath)
	}
}

func mailerTestBody(opt options) string {
	var b strings.Builder
	b.WriteString("cronwrapper mailer test\n")
	b.WriteString(fmt.Sprintf("time: %s\n", time.Now().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("host: %s\n", shortHostname()))
	b.WriteString(fmt.Sprintf("mailer: %s\n", strings.ToLower(opt.mailer)))
	if strings.EqualFold(opt.mailer, "smtp") {
		b.WriteString(fmt.Sprintf("smtp addr: %s\n", opt.smtpAddr))
		b.WriteString(fmt.Sprintf("smtp security: %s\n", strings.ToLower(opt.smtpSecurity)))
		resolved, err := resolveSMTPConfig(opt)
		if err != nil {
			b.WriteString(fmt.Sprintf("smtp resolution error: %v\n", err))
		} else {
			b.WriteString(fmt.Sprintf("smtp from: %s\n", resolved.From))
			b.WriteString(fmt.Sprintf("smtp username: %s\n", resolved.Username))
			b.WriteString(fmt.Sprintf("smtp username source: %s\n", resolved.UsernameSource))
			if resolved.Password != "" {
				b.WriteString("smtp password set: yes\n")
			} else {
				b.WriteString("smtp password set: no\n")
			}
			b.WriteString(fmt.Sprintf("smtp password source: %s\n", resolved.PasswordSource))
		}
	}
	b.WriteString("note: no wrapped command was executed; this was a mailer-only test.\n")
	return b.String()
}

func newMailer(opt options) (Mailer, error) {
	switch strings.ToLower(opt.mailer) {
	case "mailx":
		return MailxMailer{Path: opt.mailxPath}, nil
	case "smtp":
		if opt.smtpPassword != "" {
			fmt.Fprintln(os.Stderr, "warning: -smtp-password is visible to other users via process lists; prefer -smtp-password-env")
		}
		resolved, err := resolveSMTPConfig(opt)
		if err != nil {
			return nil, err
		}

		return SMTPMailer{
			Config: SMTPConfig{
				Addr:               opt.smtpAddr,
				Security:           SMTPSecurityMode(opt.smtpSecurity),
				ServerName:         opt.smtpServerName,
				InsecureSkipVerify: opt.smtpInsecureTLS,
				CACertFile:         opt.smtpCACert,
				ClientCertFile:     opt.smtpClientCert,
				ClientKeyFile:      opt.smtpClientKey,
				Username:           resolved.Username,
				Password:           resolved.Password,
				From:               resolved.From,
			},
		}, nil
	default:
		return nil, fmt.Errorf("invalid -mailer %q (expected mailx or smtp)", opt.mailer)
	}
}

func resolveSMTPConfig(opt options) (smtpResolvedConfig, error) {
	fromAddr := opt.from
	if fromAddr == "" {
		login := os.Getenv("LOGNAME")
		if login == "" {
			login = "cronwrapper"
		}
		fromAddr = fmt.Sprintf("%s@%s", login, shortHostname())
	}

	password := ""
	passwordSource := "not set"
	if opt.smtpPasswordEnv != "" {
		envPassword := os.Getenv(opt.smtpPasswordEnv)
		if envPassword != "" {
			password = envPassword
			passwordSource = fmt.Sprintf("environment (%s)", opt.smtpPasswordEnv)
		}
	}
	if opt.smtpPassword != "" {
		password = opt.smtpPassword
		passwordSource = "command line (-smtp-password)"
	}

	username := opt.smtpUsername
	usernameSource := "explicit (-smtp-username)"
	if username == "" && password != "" {
		username = fromAddr
		usernameSource = "inferred from -from/effective sender"
	}
	if username == "" {
		usernameSource = "not set"
	}

	if username != "" && password == "" {
		return smtpResolvedConfig{}, fmt.Errorf("SMTP auth requested via -smtp-username but no password found; set -smtp-password-env or -smtp-password")
	}

	return smtpResolvedConfig{
		From:           fromAddr,
		Username:       username,
		Password:       password,
		UsernameSource: usernameSource,
		PasswordSource: passwordSource,
	}, nil
}
