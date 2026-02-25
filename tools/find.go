package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore/schema"
)

// FindTool searches for files matching a glob pattern.
// Uses fd if available, falls back to filepath.WalkDir + filepath.Match.
type FindTool struct {
	WorkDir string
}

func NewFind(workDir string) *FindTool { return &FindTool{WorkDir: workDir} }

func (t *FindTool) Name() string  { return "find" }
func (t *FindTool) Label() string { return "Find Files" }
func (t *FindTool) Description() string {
	return "Search for files by glob pattern. Returns matching file paths (default limit: 1000)."
}
func (t *FindTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("pattern", schema.String("Glob pattern to match (e.g. '*.go', 'src/**/*.ts')")).Required(),
		schema.Property("path", schema.String("Directory to search in (default: working directory)")),
		schema.Property("limit", schema.Int("Maximum number of results (default: 1000)")),
	)
}

type findArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

const findDefaultLimit = 1000

func (t *FindTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a findArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	limit := a.Limit
	if limit <= 0 {
		limit = findDefaultLimit
	}

	searchDir := ResolvePath(t.WorkDir, a.Path)

	// Try fd first
	if result, err := t.findWithFd(ctx, a.Pattern, searchDir, limit); err == nil {
		return result, nil
	}

	// Fallback to filepath.WalkDir
	return t.findWithWalk(ctx, a.Pattern, searchDir, limit)
}

func (t *FindTool) findWithFd(ctx context.Context, pattern, dir string, limit int) (json.RawMessage, error) {
	fdPath, err := exec.LookPath("fd")
	if err != nil {
		return nil, err
	}

	cmdArgs := []string{
		"--glob", "--color=never", "--hidden",
		"--no-require-git",
		"--max-results", fmt.Sprintf("%d", limit),
		pattern, dir,
	}

	cmd := exec.CommandContext(ctx, fdPath, cmdArgs...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}

	return t.formatResults(string(out), dir, limit)
}

func (t *FindTool) findWithWalk(ctx context.Context, pattern, dir string, limit int) (json.RawMessage, error) {
	var matches []string

	hitLimit := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if IsSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if matched, _ := filepath.Match(pattern, d.Name()); matched {
			rel, _ := filepath.Rel(dir, path)
			matches = append(matches, rel)
		}
		if len(matches) >= limit {
			hitLimit = true
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return nil, fmt.Errorf("walk: %w", err)
	}

	if len(matches) == 0 {
		return json.Marshal("No files found matching pattern.")
	}

	output := strings.Join(matches, "\n")
	if hitLimit {
		output += fmt.Sprintf("\n\n[%d results limit reached. Use limit=%d for more, or refine pattern.]", limit, limit*2)
	}

	// Apply byte truncation
	tr := truncateHead(output, 0, defaultMaxBytes)
	if tr.Truncated {
		return json.Marshal(tr.Content + "\n\n[Output truncated at " + formatSize(defaultMaxBytes) + ".]")
	}
	return json.Marshal(output)
}

func (t *FindTool) formatResults(raw, dir string, limit int) (json.RawMessage, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var results []string
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Preserve trailing slash for directories
		suffix := ""
		if strings.HasSuffix(line, "/") || strings.HasSuffix(line, string(filepath.Separator)) {
			suffix = "/"
		}
		if rel, err := filepath.Rel(dir, strings.TrimRight(line, "/\\")); err == nil {
			results = append(results, rel+suffix)
		} else {
			results = append(results, line)
		}
		if len(results) >= limit {
			break
		}
	}

	if len(results) == 0 {
		return json.Marshal("No files found matching pattern.")
	}

	output := strings.Join(results, "\n")
	hitLimit := len(results) >= limit
	if hitLimit {
		output += fmt.Sprintf("\n\n[%d results limit reached. Use limit=%d for more, or refine pattern.]", limit, limit*2)
	}

	// Apply byte truncation
	tr := truncateHead(output, 0, defaultMaxBytes)
	if tr.Truncated {
		return json.Marshal(tr.Content + "\n\n[Output truncated at " + formatSize(defaultMaxBytes) + ".]")
	}
	return json.Marshal(output)
}
