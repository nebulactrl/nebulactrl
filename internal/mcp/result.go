package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
)

// MaxResultBytes caps the serialized size of every tool result.
const MaxResultBytes = mcpkit.MaxResultBytes

// maxTextMirror is the size up to which the text content repeats the whole
// structured result; a larger result gets a summary line instead (clients
// read the structured content).
const maxTextMirror = 8 * 1024

// Stable tool error codes that do not originate from apierr.
const (
	CodeInvalidArgument  = mcpkit.CodeInvalidArgument
	CodeOutcomeUncertain = mcpkit.CodeOutcomeUncertain
	CodeTimeout          = mcpkit.CodeTimeout
	CodeInternal         = mcpkit.CodeInternal
)

// ToolError is the structured error every failing tool call returns as
// `{code, message}` (plus optional data) in both the text content and the
// structured content of an `isError` result.
type ToolError = mcpkit.Error

func toolErr(code, format string, args ...any) *ToolError {
	return mcpkit.Errf(code, format, args...)
}

// toToolError maps any error returned by a domain service to a ToolError.
// Unexpected errors are logged and reported without internals.
func toToolError(ctx context.Context, logger *slog.Logger, tool string, err error) *ToolError {
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	if ae, ok := apierr.As(err); ok {
		switch {
		case ae.Status == http.StatusUnauthorized:
			return &ToolError{Code: "unauthorized", Message: ae.Message}
		case ae.Status >= 500 && ae.Code != "server_offline":
			logger.ErrorContext(ctx, "mcp tool failed", "tool", tool, "error", err)
			return &ToolError{Code: CodeInternal, Message: "internal error"}
		}
		te := &ToolError{Code: ae.Code, Message: ae.Message}
		if len(ae.Details) > 0 {
			te.Data = ae.Details
		}
		return te
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ToolError{Code: CodeTimeout, Message: "the operation timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &ToolError{Code: "canceled", Message: "the request was canceled"}
	}
	logger.ErrorContext(ctx, "mcp tool failed", "tool", tool, "error", err)
	return &ToolError{Code: CodeInternal, Message: "internal error"}
}

// errorResult renders te as an isError tool result.
func errorResult(te *ToolError) *mcp.CallToolResult {
	raw, _ := json.Marshal(te)
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		StructuredContent: te,
	}
}

// okResult renders v as a successful tool result: structured content (always a
// JSON object; other values are wrapped as {"result": v}) plus a text mirror.
// A value that cannot fit MaxResultBytes is replaced by a `truncated` notice.
func okResult(v any) *mcp.CallToolResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return errorResult(toolErr(CodeInternal, "internal error"))
	}
	if !isJSONObject(raw) {
		raw, err = json.Marshal(map[string]any{"result": json.RawMessage(raw)})
		if err != nil {
			return errorResult(toolErr(CodeInternal, "internal error"))
		}
	}
	if len(raw) > MaxResultBytes {
		notice := struct {
			Truncated bool   `json:"truncated"`
			Message   string `json:"message"`
		}{true, fmt.Sprintf("The result exceeded %d KB and was dropped. Narrow the request (smaller limit, fewer fields, detail=concise).", MaxResultBytes/1024)}
		nraw, _ := json.Marshal(notice)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(nraw)}}, StructuredContent: notice}
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: textMirror(raw)}},
		StructuredContent: json.RawMessage(raw),
	}
}

func isJSONObject(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		}
		return false
	}
	return false
}

// textMirror is the text content of a result: the compact JSON when small,
// otherwise a summary naming the top-level keys and list sizes.
func textMirror(raw []byte) string {
	if len(raw) <= maxTextMirror {
		return string(raw)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Sprintf("Large result (%d KB); the data is in structuredContent.", len(raw)/1024)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var list []json.RawMessage
		if json.Unmarshal(top[k], &list) == nil {
			parts = append(parts, fmt.Sprintf("%s[%d]", k, len(list)))
		} else {
			parts = append(parts, k)
		}
	}
	return fmt.Sprintf("Large result (%d KB); the full data is in structuredContent. Keys: %s.", len(raw)/1024, strings.Join(parts, ", "))
}

// listResult is the shape of every list result.
type listResult[T any] = mcpkit.List[T]

func newList[T any](items []T, next string) listResult[T] { return mcpkit.NewList(items, next) }

func fitList[T any](l listResult[T]) listResult[T] {
	l.Fit()
	return l
}

func jsonSize(v any) int { return mcpkit.JSONSize(v) }
