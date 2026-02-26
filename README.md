# cronwrapper

`cronwrapper` wraps a command, captures combined `stdout`/`stderr` to a file, and emails the output with a configurable subject line.

This mirrors the purpose of `cronwrapper.py`: keep control over email subjects (for success/failure filtering) while still receiving command output.

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

Common pattern (shell mode, default):

```bash
cronwrapper -to you@example.com 'echo out; echo err >&2'
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
- `-mailx-path string` (default `mailx`)
  - Path to `mailx` executable.
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

Mail sending is abstracted via a `Mailer` interface (`cmd/cronwrapper/mailer.go`), with current implementation using `mailx`.
This makes it straightforward to add another transport later (for example `sendmail` or SMTP).
