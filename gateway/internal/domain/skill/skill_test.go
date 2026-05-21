package skill

import (
	"context"
	"errors"
	"testing"
)

func TestValidateInput_Good(t *testing.T) {
	good := []Input{
		{SkillID: "echo", Name: "Echo"},
		{SkillID: "summarize.v2", Name: "Summarize", Tags: []string{"llm"}},
		{SkillID: "x-y_z", Name: "xyz"},
	}
	if err := ValidateInput(good); err != nil {
		t.Fatalf("should be valid: %v", err)
	}
}

func TestValidateInput_Bad(t *testing.T) {
	cases := []struct {
		name string
		in   []Input
		want error
	}{
		{"empty id", []Input{{SkillID: "", Name: "n"}}, ErrInvalidSkillID},
		{"has space", []Input{{SkillID: "has space", Name: "n"}}, ErrInvalidSkillID},
		{"single char", []Input{{SkillID: "a", Name: "n"}}, ErrInvalidSkillID},
		{"trailing dash", []Input{{SkillID: "foo-", Name: "n"}}, ErrInvalidSkillID},
		{"missing name", []Input{{SkillID: "ok.id", Name: ""}}, ErrInvalidName},
		{"duplicate", []Input{
			{SkillID: "echo", Name: "a"},
			{SkillID: "echo", Name: "b"},
		}, ErrDuplicateID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateInput(c.in)
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestAdapter_ReplaceByAgentID_Routes(t *testing.T) {
	recorded := struct {
		agent  string
		skills []Input
	}{}
	stub := &stubRepo{replace: func(ctx context.Context, agentID string, skills []Input) error {
		recorded.agent = agentID
		recorded.skills = skills
		return nil
	}}
	a := NewAdapter(stub)
	err := a.ReplaceByAgentID(context.Background(), "alice", []Input{{SkillID: "echo", Name: "Echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.agent != "alice" || len(recorded.skills) != 1 {
		t.Fatalf("routing wrong: %+v", recorded)
	}
}

func TestAdapter_ReplaceByAgentID_WrongType(t *testing.T) {
	a := NewAdapter(&stubRepo{})
	if err := a.ReplaceByAgentID(context.Background(), "alice", "not a slice"); err == nil {
		t.Fatal("expected type error")
	}
}

func TestAdapter_ReplaceByAgentID_ValidatesBeforeCall(t *testing.T) {
	called := false
	stub := &stubRepo{replace: func(ctx context.Context, agentID string, skills []Input) error {
		called = true
		return nil
	}}
	a := NewAdapter(stub)
	err := a.ReplaceByAgentID(context.Background(), "alice", []Input{{SkillID: "bad id", Name: "x"}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if called {
		t.Fatal("repo should not be called on validation failure")
	}
}

type stubRepo struct {
	replace func(ctx context.Context, agentID string, skills []Input) error
}

func (s *stubRepo) ReplaceByAgentID(ctx context.Context, agentID string, skills []Input) error {
	if s.replace == nil {
		return nil
	}
	return s.replace(ctx, agentID, skills)
}

func (s *stubRepo) ListByAgentID(ctx context.Context, agentID string) ([]*Skill, error) {
	return nil, nil
}

func (s *stubRepo) ListByAgentIDs(ctx context.Context, agentIDs []string) (map[string][]*Skill, error) {
	return map[string][]*Skill{}, nil
}
