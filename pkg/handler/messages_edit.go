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

// ConversationsUpdateMessageHandler edits an existing message via chat.update.
func (ch *ConversationsHandler) ConversationsUpdateMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ConversationsUpdateMessageHandler called", zap.Any("params", request.Params))

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	channel, ts, err := ch.parseMessageTarget(ctx, request, "conversations_update_message")
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
	// chat.update keeps a message's previous blocks unless blocks is sent, and
	// blocks win over text when rendered — so a plain edit of a message posted
	// as markdown (blocks) must clear them with an explicit empty array.
	plain := []slack.MsgOption{slack.MsgOptionDisableMarkdown(), slack.MsgOptionText(msgText, false), slack.MsgOptionBlocks([]slack.Block{}...)}
	switch contentType {
	case "text/plain":
		options = append(options, plain...)
	case "text/markdown":
		blocks, err := slackGoUtil.ConvertMarkdownTextToBlocks(msgText)
		if err != nil {
			ch.logger.Warn("Markdown parsing error", zap.Error(err))
			options = append(options, plain...)
		} else {
			// chat.update keeps fields it is not sent: without text the old
			// fallback (notifications, search) would outlive the edit.
			options = append(options, slack.MsgOptionBlocks(blocks...), slack.MsgOptionText(msgText, false))
		}
	default:
		return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
	}

	ch.logger.Debug("Updating Slack message",
		zap.String("channel", channel),
		zap.String("ts", ts),
		zap.String("content_type", contentType),
	)
	respChannel, respTimestamp, respText, err := ch.apiProvider.Slack().UpdateMessageContext(ctx, channel, ts, options...)
	if err != nil {
		ch.logger.Error("Slack UpdateMessageContext failed", zap.Error(err))
		return nil, err
	}

	// The stored text Slack echoes back is returned so a caller (e.g. a
	// post-tool render check) can see how the edit actually landed.
	return mcp.NewToolResultText(fmt.Sprintf("Successfully updated message in channel %s (ts=%s)\nStored text: %s", respChannel, respTimestamp, respText)), nil
}

// ConversationsDeleteMessageHandler deletes an existing message via chat.delete.
func (ch *ConversationsHandler) ConversationsDeleteMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ConversationsDeleteMessageHandler called", zap.Any("params", request.Params))

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	channel, ts, err := ch.parseMessageTarget(ctx, request, "conversations_delete_message")
	if err != nil {
		ch.logger.Error("Failed to parse delete-message params", zap.Error(err))
		return nil, err
	}

	// A user token deletes whatever the user could delete in the Slack client —
	// for a workspace admin that includes other members' messages. Restrict the
	// tool to the user's own messages, so a prompt-injected request cannot
	// destroy someone else's.
	if err := ch.ensureOwnMessage(ctx, channel, ts); err != nil {
		ch.logger.Warn("Refusing to delete message", zap.String("channel", channel), zap.String("ts", ts), zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Deleting Slack message",
		zap.String("channel", channel),
		zap.String("ts", ts),
	)
	respChannel, respTimestamp, err := ch.apiProvider.Slack().DeleteMessageContext(ctx, channel, ts)
	if err != nil {
		ch.logger.Error("Slack DeleteMessageContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully deleted message in channel %s (ts=%s)", respChannel, respTimestamp)), nil
}

// ensureOwnMessage fails unless the message at channel/ts was sent by the
// authenticated user. conversations.replies resolves both a top-level message
// and a thread reply by its own ts; latest=oldest=ts narrows the reply list.
func (ch *ConversationsHandler) ensureOwnMessage(ctx context.Context, channel, ts string) error {
	// AuthTest (not AuthTestContext) returns the response the client cached at
	// startup — no extra Slack round trip per delete.
	auth, err := ch.apiProvider.Slack().AuthTest()
	if err != nil {
		return fmt.Errorf("auth.test failed: %w", err)
	}
	if auth.UserID == "" {
		// Fail closed: an empty id would match author-less (bot) messages.
		return errors.New("auth.test returned no user_id; cannot verify message ownership")
	}
	msgs, _, _, err := ch.apiProvider.Slack().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: ts,
		Latest:    ts,
		Oldest:    ts,
		Inclusive: true,
	})
	if err != nil {
		// conversations.replies reports a missing ts as thread_not_found;
		// surface it as chat.delete would.
		if strings.Contains(err.Error(), "thread_not_found") {
			return errors.New("message_not_found")
		}
		return err
	}
	for _, m := range msgs {
		if m.Timestamp != ts {
			continue
		}
		if m.User != auth.UserID {
			return fmt.Errorf("cant_delete_message: message %s in %s was not sent by the authenticated user — this tool deletes only your own messages", ts, channel)
		}
		return nil
	}
	return errors.New("message_not_found")
}

// parseMessageTarget applies the same gating as conversations_add_message
// (SLACK_MCP_ADD_MESSAGE_TOOL channel policy) and validates channel_id + ts.
// ts is checked before the channel is resolved: resolving a #name/@user can
// force a full channel-list refresh, wasted on a request that fails anyway.
func (ch *ConversationsHandler) parseMessageTarget(ctx context.Context, request mcp.CallToolRequest, toolName string) (channel, ts string, err error) {
	toolConfig := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	if toolConfig == "" {
		if !strings.Contains(os.Getenv("SLACK_MCP_ENABLED_TOOLS"), toolName) {
			return "", "", fmt.Errorf(
				"by default, the %s tool is disabled to guard Slack workspaces against accidental changes. "+
					"It is gated by the same SLACK_MCP_ADD_MESSAGE_TOOL environment variable as conversations_add_message", toolName)
		}
		toolConfig = "true"
	}

	channel = request.GetString("channel_id", "")
	if channel == "" {
		return "", "", errors.New("channel_id must be a string")
	}
	ts = request.GetString("ts", "")
	if ts == "" {
		return "", "", errors.New("ts must be a string")
	}
	if !strings.Contains(ts, ".") {
		return "", "", errors.New("ts must be a valid timestamp in format 1234567890.123456")
	}

	channel, err = ch.resolveChannelID(ctx, channel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", channel), zap.Error(err))
		return "", "", err
	}
	if !isChannelAllowed(channel) {
		ch.logger.Warn("Message tool not allowed for channel", zap.String("tool", toolName), zap.String("channel", channel), zap.String("policy", toolConfig))
		return "", "", fmt.Errorf("%s tool is not allowed for channel %q, applied policy: %s", toolName, channel, toolConfig)
	}

	return channel, ts, nil
}
