package task

import (
	"testing"
)

func TestState_IsTerminal(t *testing.T) {
	cases := map[State]bool{
		StateSubmitted:     false,
		StateWorking:       false,
		StateInputRequired: false,
		StateAuthRequired:  false,
		StateCompleted:     true,
		StateCanceled:      true,
		StateFailed:        true,
		StateRejected:      true,
	}
	for s, want := range cases {
		if got := s.IsTerminal(); got != want {
			t.Errorf("IsTerminal(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestValidateTaskID(t *testing.T) {
	ok := []string{
		"abc123",
		"task-001",
		"ctx:v1.sub.2",
		"a-b_c.d:e",
		"t-bob@example-1234567890", // @ 是 agent_id 命名空间分隔符
	}
	bad := []string{
		"",
		"ab",   // 太短
		"a b",  // 空格
		"a/b",  // / 仍不允许（避免 path 歧义）
		"中文id", // 非 ASCII
		string(make([]byte, 65)),
	}
	for _, s := range ok {
		if err := ValidateTaskID(s); err != nil {
			t.Errorf("want valid for %q, got %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateTaskID(s); err == nil {
			t.Errorf("want invalid for %q", s)
		}
	}
}

func TestValidateRole(t *testing.T) {
	if err := ValidateRole(RoleUser); err != nil {
		t.Error(err)
	}
	if err := ValidateRole(RoleAgent); err != nil {
		t.Error(err)
	}
	if err := ValidateRole(Role("random")); err == nil {
		t.Error("want invalid")
	}
}

func TestValidateParts(t *testing.T) {
	if err := ValidateParts(nil); err == nil {
		t.Error("empty parts should fail")
	}
	if err := ValidateParts([]Part{{Kind: "weird"}}); err == nil {
		t.Error("weird kind should fail")
	}
	if err := ValidateParts([]Part{{Kind: PartText, Text: "hi"}}); err != nil {
		t.Error(err)
	}
	// 多 Part，混合 kind
	ok := []Part{
		{Kind: PartText, Text: "hi"},
		{Kind: PartURL, URL: "https://x"},
		{Kind: PartData, Data: map[string]any{"x": 1}},
	}
	if err := ValidateParts(ok); err != nil {
		t.Error(err)
	}
}

// 状态机核心表格驱动测试。对每个 (from, to) 组合验证。
func TestIsAllowedTransition_CompleteMatrix(t *testing.T) {
	type trans struct {
		from, to State
		want     bool
	}
	cases := []trans{
		// submitted 出边
		{StateSubmitted, StateWorking, true},
		{StateSubmitted, StateCanceled, true},
		{StateSubmitted, StateRejected, true},
		{StateSubmitted, StateCompleted, false}, // 必须先 working
		{StateSubmitted, StateFailed, false},
		{StateSubmitted, StateInputRequired, false},

		// working 出边
		{StateWorking, StateCompleted, true},
		{StateWorking, StateFailed, true},
		{StateWorking, StateCanceled, true},
		{StateWorking, StateInputRequired, true},
		{StateWorking, StateAuthRequired, true},
		{StateWorking, StateRejected, false}, // rejected 是 submitted 阶段的拒绝

		// input-required 出边
		{StateInputRequired, StateSubmitted, true},
		{StateInputRequired, StateWorking, true},
		{StateInputRequired, StateCanceled, true},
		{StateInputRequired, StateCompleted, false},

		// auth-required 出边
		{StateAuthRequired, StateSubmitted, true},
		{StateAuthRequired, StateCanceled, true},

		// 终态不能转出
		{StateCompleted, StateWorking, false},
		{StateCompleted, StateCanceled, false},
		{StateCanceled, StateSubmitted, false},
		{StateFailed, StateSubmitted, false},
		{StateRejected, StateSubmitted, false},
	}
	for _, c := range cases {
		if got := IsAllowedTransition(c.from, c.to); got != c.want {
			t.Errorf("Transition(%s→%s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// StatesAllowingTransitionTo(to) 必须返回**所有**能转到 to 的 from，
// 对 canceled 尤其关键（任意非终态都能 → canceled）。
func TestStatesAllowingTransitionTo_Canceled(t *testing.T) {
	got := StatesAllowingTransitionTo(StateCanceled)
	want := map[State]bool{
		StateSubmitted:     true,
		StateWorking:       true,
		StateInputRequired: true,
		StateAuthRequired:  true,
	}
	if len(got) != len(want) {
		t.Fatalf("want %d states, got %d (%v)", len(want), len(got), got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected: %s", s)
		}
	}
}

func TestStatesAllowingTransitionTo_Completed(t *testing.T) {
	got := StatesAllowingTransitionTo(StateCompleted)
	if len(got) != 1 || got[0] != StateWorking {
		t.Fatalf("completed only reachable from working, got %v", got)
	}
}

// 终态之间不应有路径
func TestTerminal_NoOutgoing(t *testing.T) {
	terminals := []State{StateCompleted, StateCanceled, StateFailed, StateRejected}
	allStates := []State{
		StateSubmitted, StateWorking, StateInputRequired, StateAuthRequired,
		StateCompleted, StateCanceled, StateFailed, StateRejected,
	}
	for _, from := range terminals {
		for _, to := range allStates {
			if IsAllowedTransition(from, to) {
				t.Errorf("terminal %s should not transition to anything, but allows → %s", from, to)
			}
		}
	}
}

func TestTask_peerOf(t *testing.T) {
	task := &Task{FromAgentID: "alice", ToAgentID: "bob"}
	if got := task.peerOf("alice"); got != "bob" {
		t.Errorf("alice's peer = %q", got)
	}
	if got := task.peerOf("bob"); got != "alice" {
		t.Errorf("bob's peer = %q", got)
	}
	if got := task.peerOf("charlie"); got != "" {
		t.Errorf("non-participant peer = %q, want empty", got)
	}
}
