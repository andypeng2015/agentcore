package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

// BashTool executes shell commands.
// Streams stdout+stderr via ReportToolProgress for real-time display.
// Final result applies tail truncation (2000 lines / 50KB).
type BashTool struct {
	WorkDir string
	Timeout time.Duration // default: 2 minutes
}

func NewBash(workDir string) *BashTool {
	return &BashTool{WorkDir: workDir, Timeout: 2 * time.Minute}
}

func (t *BashTool) Name() string  { return "bash" }
func (t *BashTool) Label() string { return "Execute Command" }
func (t *BashTool) Description() string {
	return fmt.Sprintf(
		"Execute a bash command. Output is truncated to last %d lines or %s (whichever is hit first). Optionally provide a timeout in seconds.",
		defaultMaxLines, formatSize(defaultMaxBytes),
	)
}
func (t *BashTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("command", schema.String("Shell command to execute")).Required(),
		schema.Property("timeout", schema.Int("Timeout in seconds (default: 120)")),
	)
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	timeout := t.Timeout
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", a.Command)
	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}
	configureProcGroup(cmd)

	// Use OS pipe so that cmd.Wait() returns as soon as the shell process
	// exits, without blocking on I/O from background subprocesses that
	// inherit the pipe's write end. (io.Pipe causes Wait to block because
	// Go internally creates goroutines to copy from OS fd to the writer,
	// and Wait waits for those goroutines.)
	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		return nil, fmt.Errorf("create pipe: %w", pipeErr)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	pw.Close() // Parent doesn't write; child processes still hold their copy.

	// Stream output line by line via tool progress
	var output []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			output = append(output, line...)
			output = append(output, '\n')
			// Report each line as progress for real-time display
			agentcore.ReportToolProgress(ctx, line)
		}
	}()

	err := cmd.Wait()

	// Shell has exited. Drain any remaining buffered output, then close
	// the read end to release the file descriptor. If a background process
	// still holds the write end open, force-close unblocks the reader.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	pr.Close()
	<-done

	outStr := string(output)
	if outStr == "" {
		outStr = "(no output)"
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// Save full output to temp file when it will be truncated
	var tempPath string
	if len(outStr) > defaultMaxBytes {
		if f, err := os.CreateTemp("", "agentcore-bash-*.log"); err == nil {
			f.WriteString(outStr)
			tempPath = f.Name()
			f.Close()
		}
	}

	// Apply tail truncation
	tr := truncateTail(outStr, defaultMaxLines, defaultMaxBytes)
	result := tr.Content
	if tr.Truncated {
		startLine := tr.TotalLines - tr.OutputLines + 1
		result += fmt.Sprintf("\n\n[Showing lines %d-%d of %d.]", startLine, tr.TotalLines, tr.TotalLines)
		if tempPath != "" {
			result += fmt.Sprintf("\n[Full output saved to: %s]", tempPath)
		}
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("%s\n\nCommand exited with code %d", result, exitCode)
	}

	return json.Marshal(result)
}
