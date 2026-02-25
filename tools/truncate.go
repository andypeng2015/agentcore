package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Truncation limit defaults.
const (
	defaultMaxLines = 2000
	defaultMaxBytes = 50 * 1024 // 50KB
)

// TruncationResult holds detailed metadata about a truncation operation.
type TruncationResult struct {
	Content               string
	Truncated             bool
	TruncatedBy           string // "lines", "bytes", or ""
	TotalLines            int
	TotalBytes            int
	OutputLines           int
	OutputBytes           int
	FirstLineExceedsLimit bool // truncateHead: first line alone exceeds byte limit
	LastLinePartial       bool // truncateTail: final line was byte-sliced
}

// truncateHead keeps the first N lines/bytes (for file reads).
// Never returns partial lines unless FirstLineExceedsLimit is true (returns empty).
func truncateHead(content string, maxLines, maxBytes int) TruncationResult {
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalBytes := len(content)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:    content,
			TotalLines: totalLines,
			TotalBytes: totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
		}
	}

	// Check if even the first line exceeds the byte limit
	if len(lines[0]) > maxBytes {
		return TruncationResult{
			Truncated:             true,
			TruncatedBy:           "bytes",
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			FirstLineExceedsLimit: true,
		}
	}

	var kept []string
	byteCount := 0
	truncatedBy := "lines"

	for i, line := range lines {
		lineBytes := len(line)
		if i > 0 {
			lineBytes++ // newline separator
		}
		if byteCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		if len(kept) >= maxLines {
			break
		}
		kept = append(kept, line)
		byteCount += lineBytes
	}

	output := strings.Join(kept, "\n")
	return TruncationResult{
		Content:     output,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(kept),
		OutputBytes: len(output),
	}
}

// truncateTail keeps the last N lines/bytes (for bash output).
// If the last line alone exceeds maxBytes, takes a byte-slice from the end
// with UTF-8 boundary safety. Sets LastLinePartial in that case.
func truncateTail(content string, maxLines, maxBytes int) TruncationResult {
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalBytes := len(content)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
		}
	}

	// Work backwards, accumulating lines
	var kept []string
	byteCount := 0
	truncatedBy := "lines"

	for i := len(lines) - 1; i >= 0 && len(kept) < maxLines; i-- {
		line := lines[i]
		lineBytes := len(line)
		if len(kept) > 0 {
			lineBytes++ // newline separator
		}
		if byteCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		kept = append([]string{line}, kept...)
		byteCount += lineBytes
	}

	// Edge case: no complete lines fit, take a UTF-8-safe tail of the last line
	if len(kept) == 0 && len(lines) > 0 {
		last := lines[len(lines)-1]
		sliced := truncateBytesFromEnd(last, maxBytes)
		return TruncationResult{
			Content:         sliced,
			Truncated:       true,
			TruncatedBy:     "bytes",
			TotalLines:      totalLines,
			TotalBytes:      totalBytes,
			OutputLines:     1,
			OutputBytes:     len(sliced),
			LastLinePartial: true,
		}
	}

	output := strings.Join(kept, "\n")
	return TruncationResult{
		Content:     output,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(kept),
		OutputBytes: len(output),
	}
}

// truncateBytesFromEnd returns the last maxBytes bytes of s,
// ensuring the result starts at a valid UTF-8 character boundary.
func truncateBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	// Advance past any UTF-8 continuation bytes (10xxxxxx pattern)
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// truncateLine truncates a single line to maxRunes rune characters.
// Uses rune-safe slicing to avoid producing invalid UTF-8.
func truncateLine(line string, maxRunes int) (string, bool) {
	if utf8.RuneCountInString(line) <= maxRunes {
		return line, false
	}
	runes := []rune(line)
	return string(runes[:maxRunes]) + "... [truncated]", true
}

func formatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}
