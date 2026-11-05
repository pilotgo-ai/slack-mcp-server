package mrkdwn

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// isolatedHome points HOME at an empty dir so the real managed hook of the
// machine running the tests never takes part.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// writeHook installs a stand-in for the managed hook under home.
func writeHook(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "hooks")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "slack-mrkdwn.py"), []byte(body), 0o755))
}

func requirePython(t *testing.T) {
	t.Helper()
	_, err := exec.LookPath("python3")
	require.NoError(t, err, "python3 is required: the formatter is a Python script")
}

// referenceFormat runs the shipped script directly, the way the hook's users do.
func referenceFormat(t *testing.T, text string) string {
	t.Helper()
	cmd := exec.Command("python3", "-I", "slack-mrkdwn.py", "format")
	cmd.Stdin = strings.NewReader(text)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		err = nil
	}
	require.NoError(t, err)
	return out.String()
}

func mustFormat(t *testing.T, text string) string {
	t.Helper()
	out, _, err := Format(context.Background(), text)
	require.NoError(t, err)
	return out
}

func TestUnitFormat_D03_Newlines(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	out := mustFormat(t, "**Pogrubione** i druga linia\nTrzecia linia\nCzwarta linia")
	assert.Equal(t, "*Pogrubione* i druga linia\nTrzecia linia\nCzwarta linia", out)
}

func TestUnitFormat_D02_KeepsMrkdwn(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	in := "*bold* i _italic_ <https://linear.app|link> <@U0123ABC> <#C0123ABC> <!here>"
	assert.Equal(t, in, mustFormat(t, in))
	assert.Equal(t, "a &lt; b &amp; c", mustFormat(t, "a < b & c"))
}

func TestUnitFormat_D06_Markdown(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	out := mustFormat(t, "## Nagłówek\n\n- a\n- b\n\n[link](https://brain.entropycore.io)")
	assert.Equal(t, "*Nagłówek*\n\n• a\n• b\n\n<https://brain.entropycore.io|link>", out)
}

func TestUnitFormat_A02_CodeUntouched(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	in := "Kod:\n```\nx = **a** < b & [c](d)\n```\ni `**inline**`"
	assert.Equal(t, in, mustFormat(t, in))
}

func TestUnitFormat_D04_Long(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("**Akapit** numer\nz drugą linią\n\n")
	}
	out := mustFormat(t, b.String())
	assert.Equal(t, 200, strings.Count(out, "*Akapit* numer\nz drugą linią"))
}

func TestUnitFormat_A03_SameAsFormatter(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	for _, in := range []string{
		"**Pogrubione** i *kursywa*\nDruga linia\nTrzecia linia",
		"## Nagłówek\n\n- a\n  - b\n\n| x | y |\n|---|---|\n| 1 | 2 |",
		"Koszt ~5 zł, plik file_name.py i ~~skreślone~~",
		"```go\nfmt.Println(1)\n```",
	} {
		assert.Equal(t, referenceFormat(t, in), mustFormat(t, in), in)
	}
}

func TestUnitFormat_A03_EmbeddedWhenHookMissing(t *testing.T) {
	requirePython(t)
	isolatedHome(t)
	assert.Equal(t, "*b*", mustFormat(t, "**b**"))
}

func TestUnitFormat_A03_EmbeddedWhenHomeUnset(t *testing.T) {
	requirePython(t)
	t.Setenv("HOME", "")
	require.NoError(t, os.Unsetenv("HOME"))
	assert.Equal(t, "*b*", mustFormat(t, "**b**"))
}

func TestUnitFormat_A03_PrefersHookFile(t *testing.T) {
	requirePython(t)
	home := isolatedHome(t)
	writeHook(t, home, "import sys\nsys.stdin.read()\nsys.stdout.write('FROM HOOK')\n")
	assert.Equal(t, "FROM HOOK", mustFormat(t, "**b**"))
}

func TestUnitFormat_A04_HookFailsGivesRaw(t *testing.T) {
	requirePython(t)
	home := isolatedHome(t)
	writeHook(t, home, "import sys\nsys.exit(2)\n")
	_, _, err := Format(context.Background(), "**b**")
	require.Error(t, err)
	assert.Equal(t, "**b**", FormatOrRaw(context.Background(), zap.NewNop(), "**b**"))
}

func TestUnitFormat_A03_EmbeddedFitsArgv(t *testing.T) {
	// The embedded copy is passed as one argv element; Linux caps one at 128 KiB.
	assert.Less(t, len(embeddedScript), 100*1024)
	sum, err := os.ReadFile("slack-mrkdwn.py")
	require.NoError(t, err)
	assert.Equal(t, string(sum), embeddedScript)
}

func TestUnitFormat_A04_FallbackOnError(t *testing.T) {
	requirePython(t)

	t.Run("python3 missing", func(t *testing.T) {
		isolatedHome(t)
		t.Setenv("PATH", t.TempDir())
		_, _, err := Format(context.Background(), "**b**")
		require.Error(t, err)
		assert.Equal(t, "**b**", FormatOrRaw(context.Background(), zap.NewNop(), "**b**"))
	})

	t.Run("private-use character rejected by the formatter", func(t *testing.T) {
		isolatedHome(t)
		in := "**b** \U000F0000"
		_, _, err := Format(context.Background(), in)
		require.Error(t, err)
		assert.Equal(t, in, FormatOrRaw(context.Background(), zap.NewNop(), in))
	})

	t.Run("whitespace-only text", func(t *testing.T) {
		isolatedHome(t)
		_, _, err := Format(context.Background(), "   ")
		require.Error(t, err)
		assert.Equal(t, "   ", FormatOrRaw(context.Background(), zap.NewNop(), "   "))
	})

	t.Run("timeout", func(t *testing.T) {
		home := isolatedHome(t)
		writeHook(t, home, "import time\ntime.sleep(5)\n")
		prev := timeout
		timeout = 200 * time.Millisecond
		t.Cleanup(func() { timeout = prev })
		start := time.Now()
		_, _, err := Format(context.Background(), "**b**")
		require.Error(t, err)
		assert.Less(t, time.Since(start), 3*time.Second)
		assert.Equal(t, "**b**", FormatOrRaw(context.Background(), zap.NewNop(), "**b**"))
	})

	t.Run("stdout over the cap", func(t *testing.T) {
		home := isolatedHome(t)
		writeHook(t, home, "import sys\nsys.stdin.read()\nsys.stdout.write('x' * (5 * 1024 * 1024))\n")
		_, _, err := Format(context.Background(), "**b**")
		require.Error(t, err)
		assert.Equal(t, "**b**", FormatOrRaw(context.Background(), zap.NewNop(), "**b**"))
	})
}

func TestUnitFormat_LintErrorIsSuccess(t *testing.T) {
	requirePython(t)
	home := isolatedHome(t)
	writeHook(t, home, "import sys\nsys.stdin.read()\nsys.stdout.write('out')\nsys.stderr.write('ERROR line 1: x')\nsys.exit(1)\n")
	out, warnings, err := Format(context.Background(), "in")
	require.NoError(t, err)
	assert.Equal(t, "out", out)
	assert.Contains(t, warnings, "ERROR line 1")

	core, logs := observer.New(zap.WarnLevel)
	assert.Equal(t, "out", FormatOrRaw(context.Background(), zap.New(core), "in"))
	require.Equal(t, 1, logs.Len())
	assert.Equal(t, int64(1), logs.All()[0].ContextMap()["errors"])
	assert.NotContains(t, logs.All()[0].ContextMap(), "findings", "lint output quotes the message and must not be logged")
}

func TestUnitFormat_NoSlackTokensInFormatterEnv(t *testing.T) {
	requirePython(t)
	home := isolatedHome(t)
	t.Setenv("SLACK_MCP_XOXC_TOKEN", "xoxc-secret")
	writeHook(t, home, "import os,sys\nsys.stdin.read()\nsys.stdout.write(','.join(sorted(k for k in os.environ if k.startswith('SLACK'))) or 'none')\n")
	assert.Equal(t, "none", mustFormat(t, "x"))
}
