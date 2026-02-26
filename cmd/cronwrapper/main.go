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
	timestamp       bool
	notimestamp     bool
	subject         string
	subjectTemplate string
	outputPath      string
	keepOutput      bool
	mailxPath       string
	debug           bool
}

type subjectData struct {
	Result  string
	Host    string
	Command string
	Code    int
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
	fs.BoolVar(&opt.timestamp, "timestamp", true, "add start/end timestamps in header")
	fs.BoolVar(&opt.notimestamp, "notimestamp", false, "disable timestamps in header")
	fs.BoolVar(&opt.debug, "debug", false, "enable debug logging")
	fs.BoolVar(&opt.keepOutput, "keep-output", false, "keep output file when using temp capture")
	fs.StringVar(&opt.subject, "subject", "", "explicit email subject (overrides -subject-template)")
	fs.StringVar(&opt.subjectTemplate, "subject-template", opt.subjectTemplate, "Go template for subject: {{.Result}} {{.Host}} {{.Command}} {{.Code}}")
	fs.StringVar(&opt.outputPath, "output", "", "capture file path (default: temp file)")
	fs.StringVar(&opt.mailxPath, "mailx-path", "mailx", "mailx executable path")

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
	if len(cmdArgs) == 0 {
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
	mailer := MailxMailer{Path: opt.mailxPath}
	if err := mailer.Send(context.Background(), rcpt, subj, body); err != nil {
		fmt.Fprintln(os.Stderr, "email command failed:", err)
		os.Exit(1)
	}

	if opt.debug {
		fmt.Fprintf(os.Stderr, "debug: command exit code=%d, output=%s\n", exitCode, capturePath)
	}
}
