package handler

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestMain isolates HOME for the whole package: markdown sends run the managed
// slack-mrkdwn hook from $HOME when present, so a developer machine or a Brain
// session would otherwise feed its own hook version into these tests.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "handler-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// postMockSlack records chat.postMessage calls (and chat.update, via editMockSlack).
type postMockSlack struct {
	editMockSlack
	postValues url.Values
}

func (m *postMockSlack) PostMessageContext(_ context.Context, channel string, options ...slack.MsgOption) (string, string, error) {
	_, values, err := slack.UnsafeApplyMsgOptions("", channel, "", options...)
	if err != nil {
		return "", "", err
	}
	m.postValues = values
	return channel, "1700000000.000200", nil
}

// requirePython: markdown sends format through python3; without it they fall
// back to raw text and the assertions below would fail with a misleading diff.
func requirePython(t *testing.T) {
	t.Helper()
	_, err := exec.LookPath("python3")
	require.NoError(t, err, "python3 is required: markdown is formatted by a Python script")
}

func addMessage(t *testing.T, args map[string]any) *postMockSlack {
	t.Helper()
	requirePython(t)
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
	m := &postMockSlack{}
	h := NewConversationsHandler(provider.NewWithClient(m, zap.NewNop()), zap.NewNop())
	_, err := h.ConversationsAddMessageHandler(context.Background(), toolRequest(args))
	require.NoError(t, err)
	return m
}

func TestUnitAddMessage_A01_MarkdownAsTextNoBlocks(t *testing.T) {
	m := addMessage(t, map[string]any{
		"channel_id": "D0123456789",
		"text":       "**Pogrubione** i druga linia\nTrzecia linia\n[link](https://example.com) <https://x.pl|gotowy>",
	})
	assert.Equal(t, "*Pogrubione* i druga linia\nTrzecia linia\n<https://example.com|link> <https://x.pl|gotowy>", m.postValues.Get("text"))
	assert.Empty(t, m.postValues.Get("blocks"))
	assert.NotEqual(t, "false", m.postValues.Get("mrkdwn"))
}

func TestUnitAddMessage_D04_LongNoBlocks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("- punkt\n\nakapit\n\n")
	}
	m := addMessage(t, map[string]any{"channel_id": "D0123456789", "text": b.String()})
	assert.Empty(t, m.postValues.Get("blocks"))
	assert.Equal(t, 200, strings.Count(m.postValues.Get("text"), "• punkt"))
}

func TestUnitAddMessage_D07_PlainAndBlocksUnchanged(t *testing.T) {
	m := addMessage(t, map[string]any{"channel_id": "D0123456789", "text": "**raw**", "content_type": "text/plain"})
	assert.Equal(t, "**raw**", m.postValues.Get("text"))
	assert.Equal(t, "false", m.postValues.Get("mrkdwn"))

	blocks := `[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`
	m = addMessage(t, map[string]any{"channel_id": "D0123456789", "text": "**fallback**", "blocks": blocks})
	assert.Contains(t, m.postValues.Get("blocks"), `"text":"hi"`)
	assert.Equal(t, "**fallback**", m.postValues.Get("text"))
}

func TestUnitAddMessage_D07_SameAsEdit(t *testing.T) {
	in := "## Plan\n- a\n- b\nkoniec & <@U0123ABC>"
	m := addMessage(t, map[string]any{"channel_id": "D0123456789", "text": in})

	e := &editMockSlack{}
	_, err := newEditHandler(e).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
		"channel_id": "D0123456789",
		"ts":         "1700000000.000100",
		"text":       in,
	}))
	require.NoError(t, err)
	assert.Equal(t, m.postValues.Get("text"), e.updateValues.Get("text"))
}
