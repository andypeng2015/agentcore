package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	if t.WorkDir != "" {
		info, err := os.Stat(t.WorkDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("working directory does not exist: %s", t.WorkDir)
			}
			return nil, fmt.Errorf("check working directory %s: %w", t.WorkDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("working directory is not a directory: %s", t.WorkDir)
		}
	}

	shellPath, shellArgs, err := resolveShell()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdArgs := append(append([]string{}, shellArgs...), a.Command)
	cmd := exec.CommandContext(ctx, shellPath, cmdArgs...)
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

	// Stream output in chunks, report progress by complete lines.
	var output []byte
	var readErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		pending := make([]byte, 0, 4096)
		for {
			n, readE := pr.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				output = append(output, chunk...)

				pending = append(pending, chunk...)
				for {
					idx := bytes.IndexByte(pending, '\n')
					if idx < 0 {
						break
					}
					agentcore.ReportToolProgress(ctx, append([]byte(nil), pending[:idx]...))
					pending = pending[idx+1:]
				}
			}
			if readE != nil {
				if !(errors.Is(readE, io.EOF) || errors.Is(readE, os.ErrClosed)) {
					readErr = readE
				} else if len(pending) > 0 {
					agentcore.ReportToolProgress(ctx, append([]byte(nil), pending...))
				}
				return
			}
		}
	}()

	err = cmd.Wait()

	// Shell has exited. Drain any remaining buffered output, then close
	// the read end to release the file descriptor. If a background process
	// still holds the write end open, force-close unblocks the reader.
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	pr.Close()
	<-done

	if readErr != nil {
		return nil, fmt.Errorf("read command output: %w", readErr)
	}

	outStr := string(output)
	if outStr == "" {
		outStr = "(no output)"
	}

	// Save full output to temp file when it will be truncated
	var tempPath string
	if len(outStr) > defaultMaxBytes {
		if f, ferr := os.CreateTemp("", "agentcore-bash-*.log"); ferr == nil {
			f.WriteString(outStr)
			tempPath = f.Name()
			f.Close()
		}
	}

	tr := truncateTail(outStr, defaultMaxLines, defaultMaxBytes)
	result := tr.Content

	if tr.Truncated {
		startLine := tr.TotalLines - tr.OutputLines + 1
		if startLine < 1 {
			startLine = 1
		}
		result += fmt.Sprintf("\n\n[Showing lines %d-%d of %d.]", startLine, tr.TotalLines, tr.TotalLines)
		if tempPath != "" {
			result += fmt.Sprintf("\n[Full output saved to: %s]", tempPath)
		}
	}

	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s\n\nCommand timed out after %d seconds", result, int(timeout.Seconds()))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("%s\n\nCommand aborted", result)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s\n\nCommand exited with code %d", result, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("%s\n\nCommand failed: %v", result, err)
	}

	return json.Marshal(result)
}

func resolveShell() (string, []string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, []string{"-c"}, nil
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p, []string{"-c"}, nil
	}
	return "", nil, fmt.Errorf("no shell found: tried bash and sh")
}
