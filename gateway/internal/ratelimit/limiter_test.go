package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_Allow_UnderLimit(t *testing.T) {
	l := NewLocalLimiter(Config{RequestsPerSecond: 10, BurstSize: 10})
	for i := 0; i < 10; i++ {
		if !l.Allow("alice") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
}

func TestLimiter_Allow_OverLimit(t *testing.T) {
	l := NewLocalLimiter(Config{RequestsPerSecond: 5, BurstSize: 5})
	for i := 0; i < 5; i++ {
		l.Allow("alice")
	}
	if l.Allow("alice") {
		t.Fatal("should be rejected after burst exhausted")
	}
}

func TestLimiter_Allow_Refill(t *testing.T) {
	l := NewLocalLimiter(Config{RequestsPerSecond: 10, BurstSize: 5})
	for i := 0; i < 5; i++ {
		l.Allow("alice")
	}
	if l.Allow("alice") {
		t.Fatal("should be rejected")
	}
	time.Sleep(200 * time.Millisecond)
	if !l.Allow("alice") {
		t.Fatal("should be allowed after refill")
	}
}

func TestLimiter_Allow_PerAgent_Isolated(t *testing.T) {
	l := NewLocalLimiter(Config{RequestsPerSecond: 5, BurstSize: 3})
	for i := 0; i < 3; i++ {
		l.Allow("alice")
	}
	if l.Allow("alice") {
		t.Fatal("alice should be rejected")
	}
	if !l.Allow("bob") {
		t.Fatal("bob should be allowed (independent bucket)")
	}
}
