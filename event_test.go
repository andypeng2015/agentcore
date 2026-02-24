package agentcore

import (
	"encoding/json"
	"testing"
)

func TestRepairMessageSequence_OrphanedToolCall(t *testing.T) {
	msgs := []Message{
		UserMsg("hi"),
		{
			Role: RoleAssistant,
			Content: []ContentBlock{
				ToolCallBlock(ToolCall{ID: "tc1", Name: "test", Args: json.RawMessage(`{}`)}),
			},
			StopReason: StopReasonToolUse,
		},
		// No tool result for tc1
	}

	repaired := RepairMessageSequence(msgs)

	if len(repaired) != 3 {
		t.Fatalf("expected 3 messages (synthetic result inserted), got %d", len(repaired))
	}
	if repaired[2].Role != RoleTool {
		t.Fatalf("expected synthetic tool result, got role %s", repaired[2].Role)
	}
}

func TestRepairMessageSequence_OrphanedToolResult(t *testing.T) {
	msgs := []Message{
		UserMsg("hi"),
		ToolResultMsg("no-matching-call", json.RawMessage(`"orphan"`), false),
	}

	repaired := RepairMessageSequence(msgs)

	// Orphaned result should be removed
	if len(repaired) != 1 {
		t.Fatalf("expected 1 message (orphan removed), got %d", len(repaired))
	}
}

func TestRepairMessageSequence_Intact(t *testing.T) {
	msgs := []Message{
		UserMsg("hi"),
		{
			Role: RoleAssistant,
			Content: []ContentBlock{
				ToolCallBlock(ToolCall{ID: "tc1", Name: "test", Args: json.RawMessage(`{}`)}),
			},
			StopReason: StopReasonToolUse,
		},
		ToolResultMsg("tc1", json.RawMessage(`"ok"`), false),
	}

	repaired := RepairMessageSequence(msgs)

	// No repair needed
	if len(repaired) != 3 {
		t.Fatalf("expected 3 messages (intact), got %d", len(repaired))
	}
}
