package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"strings"

	_ "image/gif"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// supportedImageMIME is the whitelist of image types we send to the LLM.
var supportedImageMIME = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// ReadTool reads file contents with optional offset and limit.
// Supports image files (JPEG, PNG, GIF, WebP) by returning ImageContent blocks.
// Text files apply head truncation (2000 lines / 50KB).
type ReadTool struct {
	WorkDir string
}

func NewRead(workDir string) *ReadTool { return &ReadTool{WorkDir: workDir} }

func (t *ReadTool) Name() string  { return "read" }
func (t *ReadTool) Label() string { return "Read File" }
func (t *ReadTool) Description() string {
	return fmt.Sprintf(
		"Read file contents or view images. Text output is truncated to %d lines or %s. Use offset/limit for large files. Supports JPEG, PNG, GIF, WebP images.",
		defaultMaxLines, formatSize(defaultMaxBytes),
	)
}
func (t *ReadTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("path", schema.String("Path to the file to read (relative or absolute)")).Required(),
		schema.Property("offset", schema.Int("Line number to start reading from (1-based, default: 1)")),
		schema.Property("limit", schema.Int("Maximum number of lines to read")),
	)
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Execute returns a text-only result (for backward compatibility / middleware).
func (t *ReadTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	a.Path = ResolvePath(t.WorkDir, a.Path)
	result, err := t.readText(a)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// ExecuteContent returns rich content blocks (text or image).
// Implements agentcore.ContentTool.
func (t *ReadTool) ExecuteContent(_ context.Context, args json.RawMessage) ([]agentcore.ContentBlock, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	a.Path = ResolvePath(t.WorkDir, a.Path)

	// Check if file is a supported image
	if mime := detectImageMIME(a.Path); mime != "" {
		return t.readImage(a.Path, mime)
	}

	// Text path
	result, err := t.readText(a)
	if err != nil {
		return nil, err
	}
	return []agentcore.ContentBlock{agentcore.TextBlock(result)}, nil
}

// readImage reads a file as an image, optionally resizes, and returns content blocks.
func (t *ReadTool) readImage(path, mime string) ([]agentcore.ContentBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	note := fmt.Sprintf("Read image file [%s] (%s)", mime, formatSize(len(data)))

	// Auto-resize large images to reduce token usage
	resized, resMIME, resNote := resizeImage(data, mime)
	if resNote != "" {
		data = resized
		mime = resMIME
		note += " " + resNote
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return []agentcore.ContentBlock{
		agentcore.TextBlock(note),
		agentcore.ImageBlock(encoded, mime),
	}, nil
}

const imageMaxDim = 2000

// resizeImage downscales an image if either dimension exceeds imageMaxDim.
// Returns original data unchanged if no resize is needed or on error.
func resizeImage(data []byte, mime string) ([]byte, string, string) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime, ""
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= imageMaxDim && h <= imageMaxDim {
		return data, mime, ""
	}

	// Scale proportionally
	scale := float64(imageMaxDim) / float64(max(w, h))
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	// Encode JPEG
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return data, mime, ""
	}

	// Encode PNG and pick smaller
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, dst); err == nil && pngBuf.Len() < jpegBuf.Len() {
		return pngBuf.Bytes(), "image/png", fmt.Sprintf("[Resized %dx%d → %dx%d]", w, h, newW, newH)
	}

	return jpegBuf.Bytes(), "image/jpeg", fmt.Sprintf("[Resized %dx%d → %dx%d]", w, h, newW, newH)
}

// readText reads a text file with offset/limit and returns formatted content.
func (t *ReadTool) readText(a readArgs) (string, error) {
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", a.Path, err)
	}

	allLines := strings.Split(string(data), "\n")
	totalFileLines := len(allLines)

	// Apply offset (1-based)
	startLine := 0
	if a.Offset > 0 {
		startLine = a.Offset - 1
	}
	if startLine >= len(allLines) {
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines)", a.Offset, totalFileLines)
	}

	lines := allLines[startLine:]

	// Apply user limit if specified
	userLimited := false
	if a.Limit > 0 && a.Limit < len(lines) {
		lines = lines[:a.Limit]
		userLimited = true
	}

	content := strings.Join(lines, "\n")

	// Apply head truncation
	tr := truncateHead(content, defaultMaxLines, defaultMaxBytes)

	// Handle edge case: first line alone exceeds byte limit
	if tr.FirstLineExceedsLimit {
		return fmt.Sprintf("[File %s: first line exceeds %s limit. Use offset/limit to read in chunks.]",
			a.Path, formatSize(defaultMaxBytes)), nil
	}

	// Format with line numbers
	truncatedLines := strings.Split(tr.Content, "\n")
	var sb strings.Builder
	for i, line := range truncatedLines {
		fmt.Fprintf(&sb, "%d\t%s\n", startLine+i+1, line)
	}

	result := sb.String()

	// Add truncation notice
	if tr.Truncated {
		endLine := startLine + tr.OutputLines
		nextOffset := endLine + 1
		result += fmt.Sprintf("\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
			startLine+1, endLine, totalFileLines, nextOffset)
	} else if userLimited && startLine+a.Limit < totalFileLines {
		remaining := totalFileLines - (startLine + a.Limit)
		nextOffset := startLine + a.Limit + 1
		result += fmt.Sprintf("\n[%d more lines in file. Use offset=%d to continue.]",
			remaining, nextOffset)
	}

	return result, nil
}

// detectImageMIME sniffs the file's content type and returns the MIME type
// if it's a supported image format, or "" otherwise.
func detectImageMIME(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	// http.DetectContentType needs at most 512 bytes
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return ""
	}

	mime := http.DetectContentType(buf[:n])
	if supportedImageMIME[mime] {
		return mime
	}
	return ""
}
