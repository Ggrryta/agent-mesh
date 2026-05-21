package apikey

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateRaw_FormatAndEntropy(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		raw, err := generateRaw()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !strings.HasPrefix(raw, rawKeyPrefix+"_") {
			t.Fatalf("missing prefix: %s", raw)
		}
		// 32 字节 base64url 至少 43 字符 + "sk-am_" 前缀
		if len(raw) < 40 {
			t.Fatalf("raw too short: %d", len(raw))
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("collision in 100 generations")
		}
		seen[raw] = struct{}{}
	}
}

func TestHashRaw_Deterministic(t *testing.T) {
	raw, _ := generateRaw()
	a, b := hashRaw(raw), hashRaw(raw)
	if a != b {
		t.Fatalf("hash not deterministic")
	}
	if len(a) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(a))
	}
}

func TestHashRaw_Distinguishes(t *testing.T) {
	r1, _ := generateRaw()
	r2, _ := generateRaw()
	if hashRaw(r1) == hashRaw(r2) {
		t.Fatalf("distinct keys produced same hash")
	}
}

func TestExtractPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sk-am_abcdefghijklmn", "sk-am_abcdefghij"},
		{"short", "short"},
	}
	for _, c := range cases {
		if got := extractPrefix(c.in); got != c.want {
			t.Errorf("extractPrefix(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestValidateFormat(t *testing.T) {
	ok := []string{
		"sk-am_abcdef1234567890",
		"sk-am_" + strings.Repeat("a", 32),
	}
	bad := []string{
		"",
		"sk-am",
		"sk-am_",
		"wrong-prefix_abcdef1234",
		"sk-am_short",
	}
	for _, s := range ok {
		if err := validateFormat(s); err != nil {
			t.Errorf("want valid, got error for %q: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validateFormat(s); err == nil {
			t.Errorf("want invalid, got nil for %q", s)
		}
	}
}

func TestKey_IsActive(t *testing.T) {
	k := &Key{}
	if !k.IsActive() {
		t.Fatal("new key should be active")
	}
	now := time.Now()
	k.RevokedAt = &now
	if k.IsActive() {
		t.Fatal("revoked key should not be active")
	}
}
