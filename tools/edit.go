package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/voocel/agentcore/schema"
)

// EditTool performs exact string replacement in a file.
// Supports line ending normalization, fuzzy matching, and returns unified diff.
type EditTool struct {
	WorkDir string
}

func NewEdit(workDir string) *EditTool { return &EditTool{WorkDir: workDir} }

func (t *EditTool) Name() string  { return "edit" }
func (t *EditTool) Label() string { return "Edit File" }
func (t *EditTool) Description() string {
	return "Edit a file by replacing exact text. The oldText must match exactly (including whitespace). Use this for precise, surgical edits."
}
func (t *EditTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("path", schema.String("Path to the file to edit (relative or absolute)")).Required(),
		schema.Property("old_text", schema.String("Exact text to find and replace (must be unique in the file)")).Required(),
		schema.Property("new_text", schema.String("New text to replace the old text with")).Required(),
	)
}

type editArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// editResult holds the parsed and computed edit state, shared by Preview and Execute.
type editResult struct {
	path       string
	bom        string
	ending     string // original line ending
	oldContent string // normalized-to-LF content before edit
	newContent string
}

// parseAndMatch reads the file, finds the match, and computes the replacement.
func (t *EditTool) parseAndMatch(args json.RawMessage) (*editResult, error) {
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	a.Path = ResolvePath(t.WorkDir, a.Path)

	data, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", a.Path)
	}

	raw := string(data)
	bom, raw := stripBOM(raw)

	originalEnding := detectLineEnding(raw)
	content := normalizeToLF(raw)
	oldText := normalizeToLF(a.OldText)
	newText := normalizeToLF(a.NewText)

	idx, matchLen := fuzzyFind(content, oldText)
	if idx < 0 {
		return nil, fmt.Errorf("could not find the exact text in %s. The old text must match exactly including all whitespace and newlines", a.Path)
	}

	if count := strings.Count(normalizeForFuzzy(content), normalizeForFuzzy(oldText)); count > 1 {
		return nil, fmt.Errorf("found %d occurrences of the text in %s. The text must be unique. Provide more context", count, a.Path)
	}

	newContent := content[:idx] + newText + content[idx+matchLen:]
	if content == newContent {
		return nil, fmt.Errorf("no changes made to %s. The replacement produced identical content", a.Path)
	}

	return &editResult{
		path:       a.Path,
		bom:        bom,
		ending:     originalEnding,
		oldContent: content,
		newContent: newContent,
	}, nil
}

// Preview computes the diff without writing the file.
func (t *EditTool) Preview(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := t.parseAndMatch(args)
	if err != nil {
		return nil, err
	}
	diff, firstLine := generateDiff(r.oldContent, r.newContent)
	return json.Marshal(map[string]any{
		"diff":               diff,
		"first_changed_line": firstLine,
	})
}

func (t *EditTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := t.parseAndMatch(args)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	finalContent := r.bom + restoreLineEndings(r.newContent, r.ending)
	if err := os.WriteFile(r.path, []byte(finalContent), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", r.path, err)
	}

	diff, firstLine := generateDiff(r.oldContent, r.newContent)
	return json.Marshal(map[string]any{
		"message":            fmt.Sprintf("Successfully replaced text in %s.", r.path),
		"diff":               diff,
		"first_changed_line": firstLine,
	})
}

// --- Line ending utilities ---

func detectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 || crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// --- BOM ---

func stripBOM(s string) (bom, text string) {
	if strings.HasPrefix(s, "\uFEFF") {
		return "\uFEFF", s[len("\uFEFF"):]
	}
	return "", s
}

// --- Fuzzy matching ---

// normalizeRuneForFuzzy normalizes one rune for fuzzy matching.
func normalizeRuneForFuzzy(r rune) rune {
	switch r {
	case '\u2018', '\u2019', '\u201A', '\u201B':
		return '\''
	case '\u201C', '\u201D', '\u201E', '\u201F':
		return '"'
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
		return '-'
	}
	for _, s := range unicodeSpaces {
		if r == s {
			return ' '
		}
	}
	return r
}

// normalizeForFuzzy strips trailing whitespace per line and normalizes
// smart quotes, dashes, Unicode spaces to ASCII equivalents.
func normalizeForFuzzy(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		lines[i] = strings.Map(normalizeRuneForFuzzy, line)
	}
	return strings.Join(lines, "\n")
}

type fuzzyNormalized struct {
	runes      []rune
	runeToByte []int
}

func normalizeForFuzzyWithMap(text string) fuzzyNormalized {
	lines := strings.Split(text, "\n")
	outRunes := make([]rune, 0, len(text))
	runeToByte := make([]int, 0, len(text)+1)

	globalByte := 0
	for li, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		for relByte, r := range trimmed {
			outRunes = append(outRunes, normalizeRuneForFuzzy(r))
			runeToByte = append(runeToByte, globalByte+relByte)
		}
		if li < len(lines)-1 {
			outRunes = append(outRunes, '\n')
			runeToByte = append(runeToByte, globalByte+len(line))
		}
		globalByte += len(line)
		if li < len(lines)-1 {
			globalByte++
		}
	}

	runeToByte = append(runeToByte, len(text))
	return fuzzyNormalized{
		runes:      outRunes,
		runeToByte: runeToByte,
	}
}

func indexRuneSlice(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// fuzzyFind tries exact match first, then fuzzy match.
// Fuzzy matching is only used for locating the replacement range.
// The returned index/length always point to the original content bytes.
func fuzzyFind(content, oldText string) (idx, matchLen int) {
	if i := strings.Index(content, oldText); i >= 0 {
		return i, len(oldText)
	}

	normContent := normalizeForFuzzyWithMap(content)
	fuzzyOld := normalizeForFuzzy(oldText)
	oldRunes := []rune(fuzzyOld)
	runeIdx := indexRuneSlice(normContent.runes, oldRunes)
	if runeIdx < 0 {
		return -1, 0
	}

	if runeIdx+len(oldRunes) > len(normContent.runeToByte)-1 {
		return -1, 0
	}
	startByte := normContent.runeToByte[runeIdx]
	endByte := normContent.runeToByte[runeIdx+len(oldRunes)]
	if startByte < 0 || endByte < startByte || endByte > len(content) {
		return -1, 0
	}
	return startByte, endByte - startByte
}

// --- Diff generation ---

// generateDiff produces a unified diff with line numbers and context.
func generateDiff(oldContent, newContent string) (string, int) {
	const contextLines = 4

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// Find the first and last differing lines
	maxOld := len(oldLines)
	maxNew := len(newLines)

	// Find common prefix
	prefix := 0
	for prefix < maxOld && prefix < maxNew && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	// Find common suffix (from the end, not overlapping prefix)
	suffixOld := maxOld - 1
	suffixNew := maxNew - 1
	for suffixOld > prefix && suffixNew > prefix && oldLines[suffixOld] == newLines[suffixNew] {
		suffixOld--
		suffixNew--
	}

	firstChangedLine := prefix + 1 // 1-based

	if prefix > suffixOld+1 && prefix > suffixNew+1 {
		return "(no changes)", firstChangedLine
	}

	// Build diff output with context
	maxLineNum := max(maxOld, maxNew)
	lineNumWidth := len(fmt.Sprintf("%d", maxLineNum))

	var sb strings.Builder

	// Leading context
	ctxStart := max(prefix-contextLines, 0)
	if ctxStart < prefix {
		if ctxStart > 0 {
			fmt.Fprintf(&sb, " %*s ...\n", lineNumWidth, "")
		}
		for i := ctxStart; i < prefix; i++ {
			fmt.Fprintf(&sb, " %*d %s\n", lineNumWidth, i+1, oldLines[i])
		}
	}

	// Removed lines
	for i := prefix; i <= suffixOld; i++ {
		fmt.Fprintf(&sb, "-%*d %s\n", lineNumWidth, i+1, oldLines[i])
	}

	// Added lines
	for i := prefix; i <= suffixNew; i++ {
		fmt.Fprintf(&sb, "+%*d %s\n", lineNumWidth, i+1, newLines[i])
	}

	// Trailing context
	trailStart := suffixOld + 1
	trailEnd := min(trailStart+contextLines, maxOld)
	for i := trailStart; i < trailEnd; i++ {
		fmt.Fprintf(&sb, " %*d %s\n", lineNumWidth, i+1, oldLines[i])
	}
	if trailEnd < maxOld {
		fmt.Fprintf(&sb, " %*s ...\n", lineNumWidth, "")
	}

	return sb.String(), firstChangedLine
}
