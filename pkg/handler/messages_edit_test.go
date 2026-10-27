package handler

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// editMockSlack records chat.update / chat.delete calls. Every other SlackAPI
// method panics via the embedded nil interface, so an unexpected call fails loudly.
type editMockSlack struct {
	provider.SlackAPI

	updateChannel string
	updateTs      string
	updateValues  url.Values
	updateErr     error

	deleteChannel string
	deleteTs      string
	deleteErr     error

	// noAuthUser makes auth.test return an empty user id.
	noAuthUser bool
	// replies is what conversations.replies returns for the ownership check.
	replies       []slack.Message
	repliesErr    error
	repliesParams *slack.GetConversationRepliesParameters
}

const selfUserID = "USELF0001"

func (m *editMockSlack) AuthTest() (*slack.AuthTestResponse, error) {
	if m.noAuthUser {
		return &slack.AuthTestResponse{}, nil
	}
	return &slack.AuthTestResponse{UserID: selfUserID}, nil
}

func (m *editMockSlack) GetConversationRepliesContext(_ context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	m.repliesParams = params
	if m.repliesErr != nil {
		return nil, false, "", m.repliesErr
	}
	return m.replies, false, "", nil
}

func ownMessage(ts string) []slack.Message {
	return []slack.Message{{Msg: slack.Msg{Timestamp: ts, User: selfUserID}}}
}

func (m *editMockSlack) UpdateMessageContext(_ context.Context, channel, timestamp string, options ...slack.MsgOption) (string, string, string, error) {
	m.updateChannel = channel
	m.updateTs = timestamp
	_, values, err := slack.UnsafeApplyMsgOptions("", channel, "", options...)
	if err != nil {
		return "", "", "", err
	}
	m.updateValues = values
	if m.updateErr != nil {
		return "", "", "", m.updateErr
	}
	return channel, timestamp, "stored: " + values.Get("text"), nil
}

func (m *editMockSlack) DeleteMessageContext(_ context.Context, channel, timestamp string) (string, string, error) {
	m.deleteChannel = channel
	m.deleteTs = timestamp
	if m.deleteErr != nil {
		return "", "", m.deleteErr
	}
	return channel, timestamp, nil
}

func newEditHandler(m *editMockSlack) *ConversationsHandler {
	return NewConversationsHandler(provider.NewWithClient(m, zap.NewNop()), zap.NewNop())
}

func toolRequest(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

func TestUnitConversationsUpdateMessage(t *testing.T) {
	t.Run("plain text is sent verbatim with parse=none", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{}
		res, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id":   "D0123456789",
			"ts":           "1700000000.000100",
			"text":         "see <https://example.com|the link> & *bold*",
			"content_type": "text/plain",
		}))
		require.NoError(t, err)
		assert.Equal(t, "D0123456789", m.updateChannel)
		assert.Equal(t, "1700000000.000100", m.updateTs)
		assert.Equal(t, "see <https://example.com|the link> & *bold*", m.updateValues.Get("text"))
		assert.Equal(t, "none", m.updateValues.Get("parse"))
		assert.Equal(t, "[]", m.updateValues.Get("blocks"), "a plain edit must clear blocks the original may carry")
		text := resultText(t, res)
		assert.Contains(t, text, "Successfully updated message in channel D0123456789 (ts=1700000000.000100)")
		assert.Contains(t, text, "Stored text: stored: see <https://example.com|the link> & *bold*")
	})

	t.Run("markdown is converted to blocks like add_message", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789",
			"ts":         "1700000000.000100",
			"text":       "# Title",
		}))
		require.NoError(t, err)
		assert.NotEmpty(t, m.updateValues.Get("blocks"))
		assert.Equal(t, "# Title", m.updateValues.Get("text"), "the fallback text must be replaced too")
		assert.Equal(t, "none", m.updateValues.Get("parse"))
	})

	t.Run("disabled without SLACK_MCP_ADD_MESSAGE_TOOL", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100", "text": "x",
		}))
		require.Error(t, err)
		assert.Empty(t, m.updateChannel)
	})

	t.Run("channel outside the add-message policy is refused", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C0000000001")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100", "text": "x",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed for channel")
		assert.Empty(t, m.updateChannel)
	})

	t.Run("ts is required and validated", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		for _, ts := range []string{"", "1700000000"} {
			m := &editMockSlack{}
			_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
				"channel_id": "C0123456789", "ts": ts, "text": "x",
			}))
			require.Error(t, err, "ts=%q", ts)
			assert.Empty(t, m.updateChannel)
		}
	})

	t.Run("text is required", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Empty(t, m.updateChannel)
	})

	t.Run("slack error such as cant_update_message is passed through", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{updateErr: errors.New("cant_update_message")}
		_, err := newEditHandler(m).ConversationsUpdateMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100", "text": "x",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cant_update_message")
	})
}

func TestUnitConversationsDeleteMessage(t *testing.T) {
	t.Run("deletes by channel and ts", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{replies: ownMessage("1700000000.000100")}
		res, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "D0123456789", "ts": "1700000000.000100",
		}))
		require.NoError(t, err)
		assert.Equal(t, "D0123456789", m.deleteChannel)
		assert.Equal(t, "1700000000.000100", m.deleteTs)
		assert.Contains(t, resultText(t, res), "Successfully deleted message in channel D0123456789 (ts=1700000000.000100)")
		require.NotNil(t, m.repliesParams)
		assert.Equal(t, "1700000000.000100", m.repliesParams.Timestamp)
	})

	t.Run("thread reply is found among the thread's messages", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{replies: []slack.Message{
			{Msg: slack.Msg{Timestamp: "1700000000.000001", User: "UOTHER001"}},
			{Msg: slack.Msg{Timestamp: "1700000000.000100", User: selfUserID}},
		}}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.NoError(t, err)
		assert.Equal(t, "1700000000.000100", m.deleteTs)
	})

	t.Run("someone else's message is refused before chat.delete", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{replies: []slack.Message{{Msg: slack.Msg{Timestamp: "1700000000.000100", User: "UOTHER001"}}}}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cant_delete_message")
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("empty auth user id refuses an author-less message", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{noAuthUser: true, replies: []slack.Message{{Msg: slack.Msg{Timestamp: "1700000000.000100"}}}}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no user_id")
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("thread_not_found from conversations.replies is message_not_found", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{repliesErr: slack.SlackErrorResponse{Err: "thread_not_found"}}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Equal(t, "message_not_found", err.Error())
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("message absent from conversations.replies is message_not_found", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "message_not_found")
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("disabled without SLACK_MCP_ADD_MESSAGE_TOOL", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("channel outside the add-message policy is refused", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "!C0123456789")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("invalid ts is refused", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "abc",
		}))
		require.Error(t, err)
		assert.Empty(t, m.deleteChannel)
	})

	t.Run("message_not_found is passed through", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		m := &editMockSlack{replies: ownMessage("1700000000.000100"), deleteErr: errors.New("message_not_found")}
		_, err := newEditHandler(m).ConversationsDeleteMessageHandler(context.Background(), toolRequest(map[string]any{
			"channel_id": "C0123456789", "ts": "1700000000.000100",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "message_not_found")
	})
}
