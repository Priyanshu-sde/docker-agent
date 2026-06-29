package builtins

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/docker/docker-agent/pkg/hooks"
)

// LimitLargeToolResults is the registered name of the builtin
// tool_response_transform hook that stores oversized tool results in a per-session
// temp directory and returns a bounded tail plus a notice for the conversation.
const LimitLargeToolResults = "limit_large_tool_results"

const (
	maxToolCallResultBytes       = 50 * 1024
	largeToolCallResultTailLines = 2000
	largeToolCallResultTailBytes = 50 * 1024
)

// largeResultCategories lists the tool categories whose results can be
// arbitrarily large and are not bounded anywhere else, so they are subject
// to the oversized-result cap. filesystem and shell are the high-output
// built-in toolsets; mcp and a2a call external servers that impose no
// per-result limit of their own (unlike the openapi/api toolsets, which
// already truncate their output). Internal toolsets (memory, plan, tasks,
// think, ...) return bounded, structured results and are left untouched.
var largeResultCategories = map[string]bool{
	"filesystem": true,
	"shell":      true,
	"mcp":        true,
	"a2a":        true,
}

func limitLargeToolResults(ctx context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil {
		return nil, nil
	}

	switch in.HookEventName {
	case hooks.EventToolResponseTransform:
		return limitLargeToolResponse(ctx, in)
	case hooks.EventSessionEnd:
		if err := os.RemoveAll(largeToolResultDir(in.SessionID)); err != nil {
			slog.WarnContext(ctx, "Failed to clean up large tool result temp directory", "error", err)
		}
	}
	return nil, nil
}

func limitLargeToolResponse(ctx context.Context, in *hooks.Input) (*hooks.Output, error) {
	if !largeResultCategories[in.ToolCategory] {
		return nil, nil
	}

	payload, ok := in.ToolResponse.(string)
	if !ok || !largeToolResultLimitExceeded(payload) {
		return nil, nil
	}

	path, err := writeLargeToolResult(in.SessionID, payload)
	if err != nil {
		slog.WarnContext(ctx, "Failed to write large tool call result to temp file", "error", err)
		return nil, nil
	}

	tail := tailLargeToolResult(payload)
	updated := fmt.Sprintf(
		"Tool call result was too large (%d bytes; limit %d bytes). The full result is available in a file: %s\n\nShowing the last %d lines (up to %d bytes):\n\n%s",
		len(payload),
		maxToolCallResultBytes,
		path,
		largeToolCallResultTailLines,
		largeToolCallResultTailBytes,
		tail,
	)

	return &hooks.Output{
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName:       hooks.EventToolResponseTransform,
			UpdatedToolResponse: &updated,
		},
	}, nil
}

func largeToolResultLimitExceeded(payload string) bool {
	return len(payload) > maxToolCallResultBytes || lineCount(payload) > largeToolCallResultTailLines
}

func lineCount(payload string) int {
	if payload == "" {
		return 0
	}
	lines := strings.Count(payload, "\n")
	if !strings.HasSuffix(payload, "\n") {
		lines++
	}
	return lines
}

func writeLargeToolResult(sessionID, payload string) (string, error) {
	dir := largeToolResultDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	file, err := os.CreateTemp(dir, "tool-result-*.txt")
	if err != nil {
		return "", err
	}
	path := file.Name()

	_, writeErr := file.WriteString(payload)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	return path, nil
}

func largeToolResultDir(sessionID string) string {
	if sessionID == "" {
		sessionID = "unknown"
	}
	return filepath.Join(os.TempDir(), "docker-agent-tool-results", url.PathEscape(sessionID))
}

func tailLargeToolResult(payload string) string {
	tail := lastLines([]byte(payload), largeToolCallResultTailLines)
	if len(tail) > largeToolCallResultTailBytes {
		tail = trimToRuneStart(tail[len(tail)-largeToolCallResultTailBytes:])
	}
	return string(tail)
}

func trimToRuneStart(data []byte) []byte {
	for len(data) > 0 && !utf8.RuneStart(data[0]) {
		data = data[1:]
	}
	return data
}

func lastLines(data []byte, limit int) []byte {
	if limit <= 0 || len(data) == 0 {
		return data
	}

	lines := 0
	for i, b := range slices.Backward(data) {
		if b != '\n' {
			continue
		}
		lines++
		if lines > limit {
			return data[i+1:]
		}
	}
	return data
}
