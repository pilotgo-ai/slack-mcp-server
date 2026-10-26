package handler

// Fork addition (pilotgo-ai/slack-mcp-server): edit and delete tools for
// messages the authenticated user sent. Kept in its own file so rebasing onto
// upstream releases stays trivial. The edit handler is based on upstream PR
// #303, with parse=none added so <url|text> links survive chat.update.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	slackGoUtil "github.com/takara2314/slack-go-util"
	"go.uber.org/zap"
)

type messageTarget struct {
	channel string
	ts      string
}

// ConversationsUpdateMessageHandler edits an existing message via chat.update.
func (ch *ConversationsHandler) ConversationsUpdateMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ConversationsUpdateMessageHandler called", zap.Any("params", request.Params))

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	target, err := ch.parseMessageTarget(ctx, request, "conversations_update_message")
	if err != nil {
		ch.logger.Error("Failed to parse update-message params", zap.Error(err))
		return nil, err
	}

	msgText := request.GetString("text", "")
	if msgText == "" {
		return nil, errors.New("text must be provided")
	}
	contentType := request.GetString("content_type", "text/markdown")

	// parse=none: without it chat.update re-parses the text and escapes
	// <url|text> links, unlike chat.postMessage.
	options := []slack.MsgOption{slack.MsgOptionParse(false)}
	switch contentType {
	case "text/plain":
		options = append(options, slack.MsgOptionDisableMarkdown(), slack.MsgOptionText(msgText, false))
	case "text/markdown":
		blocks, err := slackGoUtil.ConvertMarkdownTextToBlocks(msgText)
		if err != nil {
			ch.logger.Warn("Markdown parsing error", zap.Error(err))
			options = append(options, slack.MsgOptionDisableMarkdown(), slack.MsgOptionText(msgText, false))
		} else {
			options = append(options, slack.MsgOptionBlocks(blocks...))
		}
	default:
		return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
	}

	ch.logger.Debug("Updating Slack message",
		zap.String("channel", target.channel),
		zap.String("ts", target.ts),
		zap.String("content_type", contentType),
	)
	respChannel, respTimestamp, _, err := ch.apiProvider.Slack().UpdateMessageContext(ctx, target.channel, target.ts, options...)
	if err != nil {
		ch.logger.Error("Slack UpdateMessageContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully updated message in channel %s (ts=%s)", respChannel, respTimestamp)), nil
}

// ConversationsDeleteMessageHandler deletes an existing message via chat.delete.
func (ch *ConversationsHandler) ConversationsDeleteMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ConversationsDeleteMessageHandler called", zap.Any("params", request.Params))

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	target, err := ch.parseMessageTarget(ctx, request, "conversations_delete_message")
	if err != nil {
		ch.logger.Error("Failed to parse delete-message params", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Deleting Slack message",
		zap.String("channel", target.channel),
		zap.String("ts", target.ts),
	)
	respChannel, respTimestamp, err := ch.apiProvider.Slack().DeleteMessageContext(ctx, target.channel, target.ts)
	if err != nil {
		ch.logger.Error("Slack DeleteMessageContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully deleted message in channel %s (ts=%s)", respChannel, respTimestamp)), nil
}

// parseMessageTarget applies the same gating as conversations_add_message
// (SLACK_MCP_ADD_MESSAGE_TOOL channel policy) and validates channel_id + ts.
func (ch *ConversationsHandler) parseMessageTarget(ctx context.Context, request mcp.CallToolRequest, toolName string) (*messageTarget, error) {
	toolConfig := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	if toolConfig == "" {
		if !strings.Contains(os.Getenv("SLACK_MCP_ENABLED_TOOLS"), toolName) {
			return nil, fmt.Errorf(
				"by default, the %s tool is disabled to guard Slack workspaces against accidental changes. "+
					"It is gated by the same SLACK_MCP_ADD_MESSAGE_TOOL environment variable as conversations_add_message", toolName)
		}
		toolConfig = "true"
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, errors.New("channel_id must be a string")
	}
	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}
	if !isChannelAllowed(channel) {
		ch.logger.Warn("Message tool not allowed for channel", zap.String("tool", toolName), zap.String("channel", channel), zap.String("policy", toolConfig))
		return nil, fmt.Errorf("%s tool is not allowed for channel %q, applied policy: %s", toolName, channel, toolConfig)
	}

	ts := request.GetString("ts", "")
	if ts == "" {
		return nil, errors.New("ts must be a string")
	}
	if !strings.Contains(ts, ".") {
		return nil, errors.New("ts must be a valid timestamp in format 1234567890.123456")
	}

	return &messageTarget{channel: channel, ts: ts}, nil
}
