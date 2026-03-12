package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore/schema"
)

// WriteTool writes content to a file, creating directories as needed.
type WriteTool struct {
	WorkDir string
}

func NewWrite(workDir string) *WriteTool { return &WriteTool{WorkDir: workDir} }

func (t *WriteTool) Name() string  { return "write" }
func (t *WriteTool) Label() string { return "Write File" }
func (t *WriteTool) Description() string {
	return "Write content to a file. Creates parent directories if needed. Overwrites existing files."
}
func (t *WriteTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("path", schema.String("Path to the file to write")).Required(),
		schema.Property("content", schema.String("Content to write to the file")).Required(),
	)
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	a.Path = ResolvePath(t.WorkDir, a.Path)

	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", a.Path, err)
	}

	return json.Marshal(map[string]any{
		"message": fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path),
		"preview": writePreview(a.Content, 16),
	})
}

// writePreview returns the first maxLines lines of content with line numbers prefixed by "+".
func writePreview(content string, maxLines int) string {
	lines := strings.Split(content, "\n")
	total := len(lines)
	n := min(maxLines, total)

	lineNumWidth := len(fmt.Sprintf("%d", total))
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "+%*d %s\n", lineNumWidth, i+1, lines[i])
	}
	if total > n {
		fmt.Fprintf(&sb, " %*s ... +%d more lines\n", lineNumWidth, "", total-n)
	}
	return sb.String()
}
