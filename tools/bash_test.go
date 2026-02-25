package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func runBash(t *testing.T, tool *BashTool, command string, timeout int) (string, error) {
	t.Helper()
	args := map[string]any{"command": command}
	if timeout > 0 {
		args["timeout"] = timeout
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	out, execErr := tool.Execute(context.Background(), raw)
	if execErr != nil {
		return "", execErr
	}

	var text string
	if err := json.Unmarshal(out, &text); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return text, nil
}

func TestBashTimeoutErrorMessage(t *testing.T) {
	t.Parallel()

	tool := NewBash(".")
	_, err := runBash(t, tool, "sleep 2", 1)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timed out") {
		t.Fatalf("expected timeout message, got: %v", err)
	}
}

func TestBashLongSingleLineOutputNotDropped(t *testing.T) {
	t.Parallel()

	tool := NewBash(".")
	out, err := runBash(t, tool, "yes a | tr -d '\\n' | head -c 300000", 0)
	if err != nil {
		t.Fatalf("execute bash: %v", err)
	}
	if out == "(no output)" {
		t.Fatalf("unexpected empty output for long single line")
	}
	if !strings.Contains(out, "a") {
		t.Fatalf("expected output to contain command data, got: %q", out)
	}
}

func TestBashMissingWorkDirError(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing-dir")
	tool := NewBash(missing)
	_, err := runBash(t, tool, "echo hi", 0)
	if err == nil {
		t.Fatal("expected workdir error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "working directory does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}
