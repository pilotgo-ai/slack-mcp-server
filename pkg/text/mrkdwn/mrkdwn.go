// Package mrkdwn formats Markdown into Slack mrkdwn with the same Python
// formatter Brain's managed Claude Code hook `slack-mrkdwn-gate` runs, so a
// message renders the same whether or not the session has that hook (Hermes
// and other engines do not run Claude Code hooks).
//
// The managed hook file wins when present (always the current version); the
// copy embedded here is used only when it is absent and must be refreshed in
// the next fork release whenever the gate changes.
package mrkdwn

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

//go:embed slack-mrkdwn.py
var embeddedScript string

const (
	// hookRelPath is where Brain materializes the managed hook in a session container.
	hookRelPath = ".claude/hooks/slack-mrkdwn.py"
	// maxStdout: a larger result is an error, never a silently truncated message
	// (Slack caps a message's text at 40 000 characters anyway).
	maxStdout = 4 << 20
	// maxStderr: stderr only feeds the log.
	maxStderr = 64 << 10
)

// timeout bounds one formatter run; a variable so tests can shorten it.
var timeout = 5 * time.Second

// Format returns text formatted as Slack mrkdwn. warnings carries the
// formatter's lint report when it exits 1 (formatted, but something may still
// render wrong). Any other failure returns an error.
func Format(ctx context.Context, text string) (out string, warnings string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if hook := hookPath(); hook != "" {
		cmd = exec.CommandContext(ctx, "python3", "-I", hook, "format")
	} else {
		cmd = exec.CommandContext(ctx, "python3", "-I", "-c", embeddedScript, "format")
	}
	// Only what Python needs: keeps the Slack tokens out of the child's env (a
	// same-uid child could still read /proc/<ppid>/environ - defense in depth,
	// not isolation; the hook file is the one Claude Code already runs anyway).
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "PYTHONIOENCODING=utf-8"}
	cmd.Stdin = bytes.NewReader([]byte(text))
	stdout := &capBuffer{limit: maxStdout}
	stderr := &capBuffer{limit: maxStderr, truncate: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// A grandchild holding the pipes open must not outlive the timeout.
	cmd.WaitDelay = time.Second

	runErr := cmd.Run()
	if runErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", "", fmt.Errorf("mrkdwn formatter timed out after %s", timeout)
	}
	if stdout.overflow {
		return "", "", fmt.Errorf("mrkdwn formatter output exceeds %d bytes", maxStdout)
	}
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return "", "", fmt.Errorf("mrkdwn formatter did not run: %w", runErr)
		}
		code = exitErr.ExitCode()
	}
	if (code == 0 || code == 1) && stdout.Len() > 0 {
		if code == 1 {
			warnings = stderr.String()
		}
		return stdout.String(), warnings, nil
	}
	return "", "", fmt.Errorf("mrkdwn formatter exited %d: %s", code, firstLine(stderr.String()))
}

// FormatOrRaw is Format that never loses a message: on any error the text goes
// out unchanged and the error is logged.
func FormatOrRaw(ctx context.Context, logger *zap.Logger, text string) string {
	out, warnings, err := Format(ctx, text)
	if err != nil {
		logger.Warn("mrkdwn formatting failed, sending text unchanged", zap.Error(err))
		return text
	}
	if warnings != "" {
		// Counts only: the findings quote pieces of the message.
		logger.Warn("mrkdwn formatter reported lint findings",
			zap.Int("errors", strings.Count(warnings, "ERROR line")),
			zap.Int("warnings", strings.Count(warnings, "WARN line")))
	}
	return out
}

// firstLine keeps an error short: a usage error is one line, while lint output
// could quote the message.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func hookPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	p := filepath.Join(home, hookRelPath)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// capBuffer keeps at most limit bytes. Over the limit it either flags overflow
// (stdout) or silently drops the rest (stderr, truncate=true). The buffer is a
// field, not embedded: an embedded bytes.Buffer would promote ReadFrom, and
// io.Copy in os/exec would then bypass Write and the cap.
type capBuffer struct {
	buf      bytes.Buffer
	limit    int
	truncate bool
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) > room {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		if !b.truncate {
			b.overflow = true
		}
		// Report a full write so the child is not killed by a broken pipe mid-run.
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *capBuffer) Len() int       { return b.buf.Len() }
func (b *capBuffer) String() string { return b.buf.String() }

var _ io.Writer = (*capBuffer)(nil)
