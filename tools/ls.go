package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
)

// LsTool lists directory contents with optional depth control.
type LsTool struct {
	WorkDir string
}

func NewLs(workDir string) *LsTool { return &LsTool{WorkDir: workDir} }

func (t *LsTool) Name() string  { return "ls" }
func (t *LsTool) Label() string { return "List Directory" }
func (t *LsTool) Description() string {
	return fmt.Sprintf(
		"List directory contents. Returns file/directory names with sizes, sorted alphabetically. Depth controls recursive listing (default 1, max 5). Output truncated to %d entries or %s.",
		lsDefaultLimit, formatSize(defaultMaxBytes),
	)
}
func (t *LsTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("path", schema.String("Directory path (default: working directory)")),
		schema.Property("depth", schema.Int("Recursion depth (default: 1, max: 5)")),
		schema.Property("limit", schema.Int("Maximum entries to return (default: 500)")),
	)
}

type lsArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
	Limit int    `json:"limit"`
}

const lsDefaultLimit = 500

func (t *LsTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a lsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	dir := ResolvePath(t.WorkDir, a.Path)

	depth := a.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	maxEntries := a.Limit
	if maxEntries <= 0 {
		maxEntries = lsDefaultLimit
	}

	var entries []string
	count := 0

	err := walkDepth(ctx, dir, dir, 0, depth, func(rel string, info os.FileInfo, isDir bool) bool {
		if count >= maxEntries {
			return false
		}
		count++

		if isDir {
			entries = append(entries, rel+"/")
		} else {
			entries = append(entries, fmt.Sprintf("%s  %s", rel, formatSize(int(info.Size()))))
		}
		return true
	})

	if err != nil {
		return nil, fmt.Errorf("ls %s: %w", dir, err)
	}

	if len(entries) == 0 {
		return json.Marshal("(empty directory)")
	}

	// Sort alphabetically (case-insensitive)
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i]) < strings.ToLower(entries[j])
	})

	result := strings.Join(entries, "\n")
	if count >= maxEntries {
		result += fmt.Sprintf("\n\n[Listing truncated at %d entries. Use limit=%d for more, or use a specific subdirectory.]", maxEntries, maxEntries*2)
	}

	// Apply byte truncation
	tr := truncateHead(result, 0, defaultMaxBytes)
	if tr.Truncated {
		return json.Marshal(tr.Content + "\n\n[Output truncated at " + formatSize(defaultMaxBytes) + ".]")
	}
	return json.Marshal(result)
}

func walkDepth(ctx context.Context, root, dir string, current, maxDepth int, fn func(rel string, info os.FileInfo, isDir bool) bool) error {
	if current >= maxDepth {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, e := range dirEntries {
		name := e.Name()
		if IsSkipDir(name) {
			continue
		}

		path := filepath.Join(dir, name)
		rel, _ := filepath.Rel(root, path)
		info, err := e.Info()
		if err != nil {
			continue
		}

		isDir := e.IsDir()
		if !fn(rel, info, isDir) {
			return nil
		}

		if isDir {
			if err := walkDepth(ctx, root, path, current+1, maxDepth, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
