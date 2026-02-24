package agentcore

import "testing"

func TestCost_Add(t *testing.T) {
	c := &Cost{Input: 1.0, Output: 2.0, Total: 3.0}
	c.Add(&Cost{Input: 0.5, Output: 1.0, CacheRead: 0.1, Total: 1.6})

	if c.Input != 1.5 || c.Output != 3.0 || c.CacheRead != 0.1 || c.Total != 4.6 {
		t.Fatalf("unexpected cost: %+v", c)
	}
}

func TestCost_Add_NilSafe(t *testing.T) {
	c := &Cost{Total: 1.0}
	c.Add(nil) // should not panic
	if c.Total != 1.0 {
		t.Fatalf("nil Add changed value: %+v", c)
	}
}

func TestUsage_Add_WithCost(t *testing.T) {
	u := &Usage{Input: 100, Output: 50, Cost: &Cost{Total: 1.0}}
	u.Add(&Usage{Input: 200, Output: 100, Cost: &Cost{Total: 2.0}})

	if u.Input != 300 || u.Output != 150 {
		t.Fatalf("unexpected usage: %+v", u)
	}
	if u.Cost == nil || u.Cost.Total != 3.0 {
		t.Fatalf("unexpected cost: %+v", u.Cost)
	}
}

func TestUsage_Add_CostFromNil(t *testing.T) {
	u := &Usage{Input: 10}
	u.Add(&Usage{Input: 5, Cost: &Cost{Total: 0.5}})

	if u.Cost == nil || u.Cost.Total != 0.5 {
		t.Fatalf("cost should be initialized from nil: %+v", u.Cost)
	}
}

func TestCollectMessages(t *testing.T) {
	msgs := []AgentMessage{
		UserMsg("hello"),
		SystemMsg("system"),
	}
	collected := CollectMessages(msgs)
	if len(collected) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(collected))
	}
}

func TestToAgentMessages(t *testing.T) {
	msgs := []Message{UserMsg("a"), UserMsg("b")}
	agent := ToAgentMessages(msgs)
	if len(agent) != 2 {
		t.Fatalf("expected 2, got %d", len(agent))
	}
}
