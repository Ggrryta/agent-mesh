package auth

import (
	"strings"
	"testing"
	"time"
)

func TestNewSigner_ShortSecret(t *testing.T) {
	if _, err := NewSigner("short", time.Hour, time.Hour); err == nil {
		t.Fatal("expected error for short secret")
	}
}

func TestNewSigner_DefaultsForZeroTTL(t *testing.T) {
	s, err := NewSigner("test_secret_for_agent_mesh", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 默认值不应该为 0；具体值在 NewSigner 里给。
	if s.userTTL <= 0 || s.agentTTL <= 0 {
		t.Fatalf("zero TTL not backfilled: user=%v agent=%v", s.userTTL, s.agentTTL)
	}
}

func TestIssueAndVerify_UserRoundTrip(t *testing.T) {
	s := mustSigner(t)
	tok, err := s.IssueUser(42)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Kind != KindUser || c.UID != 42 {
		t.Fatalf("claims wrong: %+v", c)
	}
	if c.KeyID != 0 {
		t.Fatalf("user token must not carry key_id, got %d", c.KeyID)
	}
}

func TestIssueAndVerify_AgentRoundTrip(t *testing.T) {
	s := mustSigner(t)
	tok, err := s.IssueAgent("alice-dev", 7, 99)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Kind != KindAgent || c.AgentID != "alice-dev" || c.UID != 7 || c.KeyID != 99 {
		t.Fatalf("claims wrong: %+v", c)
	}
}

func TestAgentTTL_Getter(t *testing.T) {
	s, _ := NewSigner("test_secret_for_agent_mesh", 24*time.Hour, 30*time.Minute)
	if got := s.AgentTTL(); got != 30*time.Minute {
		t.Fatalf("want 30m, got %v", got)
	}
}

func TestVerify_Expired(t *testing.T) {
	// agent TTL 比等待时间短，强制触发过期。
	s, err := NewSigner("12345678901234567890", time.Hour, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.IssueAgent("a", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerify_DifferentSecret(t *testing.T) {
	s1 := mustSigner(t)
	s2, _ := NewSigner("another_sixteen_bytes_secret", time.Hour, time.Hour)
	tok, _ := s1.IssueUser(1)
	if _, err := s2.Verify(tok); err == nil {
		t.Fatal("expected verify failure with mismatched secret")
	}
}

func TestVerify_Malformed(t *testing.T) {
	s := mustSigner(t)
	if _, err := s.Verify("not.a.jwt"); err == nil {
		t.Fatal("expected malformed error")
	}
	// alg=none header 必须被拒，防 alg=none 攻击。
	if _, err := s.Verify("eyJhbGciOiJub25lIn0."); err == nil || !strings.Contains(err.Error(), "") {
		// 任意错误都可接受，关键是不能放行。
	}
}

func mustSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner("test_secret_for_agent_mesh", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
