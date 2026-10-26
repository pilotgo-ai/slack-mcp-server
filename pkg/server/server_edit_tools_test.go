package server

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type authOnlySlack struct {
	provider.SlackAPI
}

func (authOnlySlack) AuthTest() (*slack.AuthTestResponse, error) {
	return &slack.AuthTestResponse{Team: "t", User: "u", URL: "https://example.slack.com/"}, nil
}

func registeredTools(t *testing.T, enabledTools []string) map[string]bool {
	t.Helper()
	s := NewMCPServer(provider.NewWithClient(authOnlySlack{}, zap.NewNop()), zap.NewNop(), enabledTools)
	names := map[string]bool{}
	for name := range s.server.ListTools() {
		names[name] = true
	}
	return names
}

func TestUnitEditDeleteToolsRegistration(t *testing.T) {
	t.Run("registered together with add_message when SLACK_MCP_ADD_MESSAGE_TOOL is set", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		tools := registeredTools(t, nil)
		assert.True(t, tools[ToolConversationsAddMessage])
		assert.True(t, tools[ToolConversationsUpdateMessage])
		assert.True(t, tools[ToolConversationsDeleteMessage])
	})

	t.Run("not registered without SLACK_MCP_ADD_MESSAGE_TOOL", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		tools := registeredTools(t, nil)
		assert.False(t, tools[ToolConversationsAddMessage])
		assert.False(t, tools[ToolConversationsUpdateMessage])
		assert.False(t, tools[ToolConversationsDeleteMessage])
	})

	t.Run("enabled individually through SLACK_MCP_ENABLED_TOOLS", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		tools := registeredTools(t, []string{ToolConversationsUpdateMessage})
		assert.True(t, tools[ToolConversationsUpdateMessage])
		assert.False(t, tools[ToolConversationsDeleteMessage])
	})
}
