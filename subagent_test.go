package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// simpleAgent creates a SubAgentConfig that always replies with the given text.
func simpleAgent(name, reply string) SubAgentConfig {
	return SubAgentConfig{
		Name:        name,
		Description: name + " agent",
		StreamFn: mockStreamFn(Message{
			Role:       RoleAssistant,
			Content:    []ContentBlock{TextBlock(reply)},
			StopReason: StopReasonStop,
		}),
		MaxTurns: 3,
	}
}

func parseResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	return out
}

func TestSubAgentTool_Single(t *testing.T) {
	tool := NewSubAgentTool(simpleAgent("writer", "hello"))
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"agent":"writer","task":"greet"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := parseResult(t, result)
	if out["output"] != "hello" {
		t.Fatalf("expected 'hello', got %v", out["output"])
	}
}

func TestSubAgentTool_UnknownAgent(t *testing.T) {
	tool := NewSubAgentTool(simpleAgent("writer", "x"))
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"agent":"unknown","task":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := parseResult(t, result)
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, "unknown agent") {
		t.Fatalf("expected unknown agent error, got %v", out)
	}
}

func TestSubAgentTool_Chain(t *testing.T) {
	tool := NewSubAgentTool(
		simpleAgent("step1", "first-output"),
		simpleAgent("step2", "final-output"),
	)
	args := `{"chain":[{"agent":"step1","task":"do A"},{"agent":"step2","task":"continue from {previous}"}]}`
	result, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	out := parseResult(t, result)
	if out["output"] != "final-output" {
		t.Fatalf("expected last chain output, got %v", out["output"])
	}
	// Verify chain produced correct number of steps
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("expected 2 chain results, got %d", len(results))
	}
}

func TestSubAgentTool_Parallel(t *testing.T) {
	tool := NewSubAgentTool(
		simpleAgent("a", "result-a"),
		simpleAgent("b", "result-b"),
	)
	args := `{"tasks":[{"agent":"a","task":"t1"},{"agent":"b","task":"t2"}]}`
	result, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	out := parseResult(t, result)
	if out["summary"] != "2/2 succeeded" {
		t.Fatalf("expected 2/2 succeeded, got %v", out["summary"])
	}
}

func TestSubAgentTool_ModeValidation(t *testing.T) {
	tool := NewSubAgentTool(simpleAgent("x", "y"))

	// No mode
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	if err := json.Unmarshal(result, &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "exactly one mode") {
		t.Fatalf("expected mode validation error, got %q", msg)
	}

	// Multiple modes
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"agent":"x","task":"t","tasks":[{"agent":"x","task":"t"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result, &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "exactly one mode") {
		t.Fatalf("expected mode validation error, got %q", msg)
	}
}
