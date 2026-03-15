package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/agentcore/schema"
)

// GrepTool searches file contents by pattern.
// Uses ripgrep (rg) if available, falls back to regexp + bufio.Scanner.
type GrepTool struct {
	WorkDir string
}

func NewGrep(workDir string) *GrepTool { return &GrepTool{WorkDir: workDir} }

func (t *GrepTool) Name() string  { return "grep" }
func (t *GrepTool) Label() string { return "Search Content" }
func (t *GrepTool) Description() string {
	return "Search file contents by regex pattern. Returns matching lines with file paths and line numbers (default limit: 100)."
}
func (t *GrepTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("pattern", schema.String("Search pattern (regex by default, or literal with literal=true)")).Required(),
		schema.Property("path", schema.String("File or directory to search (default: working directory)")),
		schema.Property("glob", schema.String("File glob filter (e.g. '*.go', '*.ts')")),
		schema.Property("ignoreCase", schema.Bool("Case insensitive search")),
		schema.Property("literal", schema.Bool("Treat pattern as literal string, not regex")),
		schema.Property("contextLines", schema.Int("Number of context lines around each match (default: 0)")),
		schema.Property("limit", schema.Int("Maximum number of matches (default: 100)")),
	)
}

type grepArgs struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	Glob         string `json:"glob"`
	IgnoreCase   bool   `json:"ignoreCase"`
	Literal      bool   `json:"literal"`
	ContextLines int    `json:"contextLines"`
	Limit        int    `json:"limit"`
}

const (
	grepDefaultLimit = 100
	grepMaxLineLen   = 500
	grepMaxBytes     = 50 * 1024
)

// rgMatchLineRe matches rg output lines that are actual matches (colon after line number),
// not context lines (dash after line number). Match: "file:42:content", Context: "file-40-content".
var rgMatchLineRe = regexp.MustCompile(`^.+:\d+:`)

func isRgMatchLine(line string) bool {
	return rgMatchLineRe.MatchString(line)
}

func (t *GrepTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a grepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Limit <= 0 {
		a.Limit = grepDefaultLimit
	}

	searchPath := ResolvePath(t.WorkDir, a.Path)

	// Try ripgrep first
	if result, err := t.grepWithRg(ctx, a, searchPath); err == nil {
		return result, nil
	}

	// Fallback to Go implementation
	return t.grepWithGo(ctx, a, searchPath)
}

// grepWithRg uses ripgrep with streaming output.
// Kills the process once the match limit is reached.
func (t *GrepTool) grepWithRg(ctx context.Context, a grepArgs, searchPath string) (json.RawMessage, error) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return nil, err
	}

	cmdArgs := []string{"--line-number", "--no-heading", "--color", "never"}

	if a.IgnoreCase {
		cmdArgs = append(cmdArgs, "--ignore-case")
	}
	if a.Literal {
		cmdArgs = append(cmdArgs, "--fixed-strings")
	}
	if a.ContextLines > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--context=%d", a.ContextLines))
	}
	if a.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", a.Glob)
	}

	cmdArgs = append(cmdArgs, a.Pattern, searchPath)

	cmd := exec.CommandContext(ctx, rgPath, cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start rg: %w", err)
	}

	prefix := searchPath + string(filepath.Separator)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var lines []string
	matchCount := 0
	hitLimit := false

	for scanner.Scan() {
		line := scanner.Text()

		// Make paths relative
		if rel, ok := strings.CutPrefix(line, prefix); ok {
			line = rel
		}

		// Truncate long lines
		if tl, truncated := truncateLine(line, grepMaxLineLen+50); truncated {
			line = tl
		}

		// Count actual matches (skip context lines and group separators)
		if line == "--" {
			// group separator, not a match
		} else if a.ContextLines > 0 {
			if isRgMatchLine(line) {
				matchCount++
			}
		} else {
			matchCount++
		}

		lines = append(lines, line)

		if matchCount >= a.Limit {
			hitLimit = true
			break
		}
	}

	// Kill rg process early if we hit the limit
	if hitLimit && cmd.Process != nil {
		cmd.Process.Kill()
	}
	cmd.Wait() //nolint: errcheck // exit code 1 (no matches) is normal, not an error

	if len(lines) == 0 {
		// rg exit code 2 (real error) writes to stderr; exit code 1 (no matches) does not.
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			return nil, fmt.Errorf("grep: %s", errMsg)
		}
		return json.Marshal("No matches found.")
	}

	result := strings.Join(lines, "\n")
	if hitLimit {
		result += fmt.Sprintf("\n\n[Results truncated at %d matches. Use a more specific pattern or path.]", a.Limit)
	}

	// Apply byte truncation
	tr := truncateHead(result, 0, grepMaxBytes)
	if tr.Truncated {
		return json.Marshal(tr.Content + "\n\n[Output truncated.]")
	}
	return json.Marshal(result)
}

func (t *GrepTool) grepWithGo(ctx context.Context, a grepArgs, searchPath string) (json.RawMessage, error) {
	pattern := a.Pattern
	if a.Literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if a.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	var results []string
	matchCount := 0
	limit := a.Limit

	err = filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// If root path itself is invalid/inaccessible, return explicit error.
			if path == searchPath {
				return err
			}
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
		if a.Glob != "" {
			if matched, _ := filepath.Match(a.Glob, d.Name()); !matched {
				return nil
			}
		}
		// Skip binary/large files
		info, _ := d.Info()
		if info != nil && info.Size() > 1024*1024 {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		rel, _ := filepath.Rel(searchPath, path)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 2*1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				if tl, truncated := truncateLine(line, grepMaxLineLen); truncated {
					line = tl
				}
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNum, line))
				matchCount++
				if matchCount >= limit {
					return filepath.SkipAll
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan %s: %w", rel, err)
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return nil, fmt.Errorf("search: %w", err)
	}

	if len(results) == 0 {
		return json.Marshal("No matches found.")
	}

	result := strings.Join(results, "\n")
	if matchCount >= limit {
		result += fmt.Sprintf("\n\n[Results truncated at %d matches. Use a more specific pattern or path.]", limit)
	}
	return json.Marshal(result)
}
