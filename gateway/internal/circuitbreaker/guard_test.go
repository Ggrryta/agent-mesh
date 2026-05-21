package circuitbreaker

import (
	"errors"
	"testing"

	"github.com/sony/gobreaker"
)

func TestGuard_Execute_Success(t *testing.T) {
	g := NewGuard(DefaultConfig())
	err := g.Execute("alice", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if g.State("alice") != gobreaker.StateClosed {
		t.Fatal("should be closed")
	}
}

func TestGuard_Execute_TripsAfterThreshold(t *testing.T) {
	cfg := Config{MaxRequests: 1, Interval: 0, Timeout: 0, FailThreshold: 3}
	g := NewGuard(cfg)

	fail := errors.New("downstream error")
	for i := 0; i < 3; i++ {
		g.Execute("bob", func() error { return fail })
	}

	if g.State("bob") != gobreaker.StateOpen {
		t.Fatalf("should be open after 3 failures, got %v", g.State("bob"))
	}

	err := g.Execute("bob", func() error { return nil })
	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Fatalf("should reject with ErrOpenState, got %v", err)
	}
}

func TestGuard_PerAgent_Isolated(t *testing.T) {
	cfg := Config{MaxRequests: 1, Interval: 0, Timeout: 0, FailThreshold: 2}
	g := NewGuard(cfg)

	fail := errors.New("fail")
	g.Execute("alice", func() error { return fail })
	g.Execute("alice", func() error { return fail })

	if g.State("alice") != gobreaker.StateOpen {
		t.Fatal("alice should be open")
	}
	if g.State("bob") != gobreaker.StateClosed {
		t.Fatal("bob should still be closed")
	}
}
