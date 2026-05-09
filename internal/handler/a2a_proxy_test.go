package handler

import (
	"encoding/json"
	"testing"
)

func TestIsStreamingMethod(t *testing.T) {
	cases := []struct {
		body     string
		expected bool
	}{
		{`{"method":"message/stream"}`, true},
		{`{"method":"tasks/resubscribe"}`, true},
		{`{"method":"message/send"}`, false},
		{`{"method":"tasks/get"}`, false},
		{`{"method":""}`, false},
		{`not json`, false},
		{`{}`, false},
	}

	for _, tc := range cases {
		got := isStreamingMethod([]byte(tc.body))
		if got != tc.expected {
			t.Errorf("isStreamingMethod(%q) = %v, want %v", tc.body, got, tc.expected)
		}
	}
}

func TestIsStreamingMethod_MalformedJSON(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte(`{`),
		[]byte(`{"method":123}`),
	}
	for _, b := range cases {
		if isStreamingMethod(b) {
			t.Errorf("expected false for malformed input %q", b)
		}
	}
}

func TestIsStreamingMethod_ExtraFields(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/stream",
		"params":  map[string]any{"foo": "bar"},
	})
	if !isStreamingMethod(body) {
		t.Fatal("expected true for message/stream with extra fields")
	}
}
