# cronwrapper

`cronwrapper` wraps a command, captures combined `stdout`/`stderr` to a file, and emails the output with a configurable subject line. By default, the subject line starts with 'SUCCESS' or 'FAILURE' based on the exit code of the command, making it easy to create mail filters while still delivering command output.

## Build

Requirements:
- Go 1.22+

Build binary:

```bash
go build -o cronwrapper ./cmd/cronwrapper
```

Install locally into `$GOBIN`/`$GOPATH/bin`:

```bash
go install ./cmd/cronwrapper
```

Install from GitHub once published:

```bash
go install github.com/andrelucas/cronwrapper-go/cmd/cronwrapper@latest
```

If you publish under a different GitHub path, update `module` in `go.mod` and use that path in `go install`.

## Usage

```text
cronwrapper [flags] command [args...]
```

`command [args...]` is required unless `-mailer-test` is set.

Common pattern (shell mode, default):

```bash
cronwrapper -to you@example.com 'echo out; echo err >&2'
```

Use SMTP backend explicitly:

```bash
cronwrapper -mailer smtp -smtp-addr smtp.example.com:587 -smtp-security starttls -smtp-username myuser -to you@example.com 'your command'
```

Test mailer configuration without running any command:

```bash
cronwrapper -mailer smtp -smtp-addr smtp.example.com:587 -smtp-security starttls -smtp-username myuser -to you@example.com -mailer-test
```

### Important command-passing behavior

- Default is shell mode (`/bin/sh -c ...`).
- In shell mode, pass your shell command as one quoted argument whenever possible.
- Use `-noshell` to execute directly without a shell.

Examples:

```bash
# shell mode (default)
cronwrapper -to you@example.com 'cmd1 | cmd2 > /tmp/x'

# direct exec mode
cronwrapper -to you@example.com -noshell /usr/bin/env printf 'hello\n'
```

## Flags

- `-to string`
  - Email recipient. Defaults to `$LOGNAME`.
- `-noshell`
  - Run command directly instead of via `/bin/sh -c`.
- `-mailer-test`
  - Send a test message using the selected mailer and exit without executing the wrapped command.
  - Useful for validating transport/auth/certificate settings safely.
  - For SMTP, diagnostics include effective username and whether password is set, plus password source (`environment` vs command line), without exposing the secret.
- `-timestamp` (default `true`)
  - Include start/end timestamps in mail header.
- `-notimestamp`
  - Disable timestamp header.
- `-subject string`
  - Explicit subject; overrides `-subject-template`.
- `-subject-template string`
  - Go template for generated subject.
  - Available fields: `{{.Result}}`, `{{.Host}}`, `{{.Command}}`, `{{.Code}}`.
  - Default: `{{.Result}}: {{.Host}}: {{.Command}}`
- `-output string`
  - Path to capture file. If omitted, a temp file is used.
- `-keep-output`
  - Keep temp capture file instead of deleting it.
- `-mailer string` (default `mailx`)
  - Mail backend: `mailx` or `smtp`.
- `-mailx-path string` (default `mailx`)
  - Path to `mailx` executable (used when `-mailer mailx`).
- `-smtp-addr string` (default `127.0.0.1:25`)
  - SMTP server address (`host:port`) used when `-mailer smtp`.
- `-smtp-security string` (default `starttls`)
  - SMTP transport security: `none`, `starttls`, or `tls`.
- `-smtp-server-name string`
  - TLS server name override (default: host from `-smtp-addr`).
- `-smtp-insecure-skip-verify`
  - Skip TLS certificate verification (discouraged).
- `-smtp-ca-cert string`
  - PEM file containing additional trusted CA certificates.
- `-smtp-client-cert string`
  - Client certificate PEM for mutual TLS.
- `-smtp-client-key string`
  - Client key PEM for mutual TLS.
- `-smtp-username string`
  - SMTP SASL username. If omitted but a password is provided, defaults to the resolved `-from` value.
- `-smtp-password string`
  - SMTP SASL password. Strongly discouraged because command-line args are visible in process lists.
- `-smtp-password-env string` (default `CRONWRAPPER_SMTP_PASSWORD`)
  - Environment variable holding SMTP SASL password.
- `-from string`
  - SMTP envelope/header sender (default `$LOGNAME@hostname`) when `-mailer smtp`.
  - Also used as default SMTP username when a password is provided without `-smtp-username`.
- `-debug`
  - Print debug info to `stderr`.

## Output capture semantics

- `stdout` and `stderr` are both written to the same file descriptor during execution.
- This preserves stream ordering as emitted by the OS/process setup as closely as possible.
- Output is not reassembled after the fact from separate buffers.

## Exit behavior

- If wrapped command exits non-zero, email subject uses `FAILURE`, and header includes `Shell return code`.
- If wrapped command exits zero, subject uses `SUCCESS`.
- If email send fails, wrapper exits non-zero and prints an error to `stderr`.

## Mail backend

Mail sending is abstracted via a `Mailer` interface in `cmd/cronwrapper/mailer.go`.

- `mailx` backend (default): local `mailx` binary.
- `smtp` backend: direct SMTP delivery via `github.com/emersion/go-smtp` and SASL auth via `github.com/emersion/go-sasl`.

For SMTP auth, prefer setting `CRONWRAPPER_SMTP_PASSWORD` (or your configured `-smtp-password-env` variable) instead of using `-smtp-password` on the command line.
