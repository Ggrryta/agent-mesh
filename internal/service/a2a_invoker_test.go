package service

import (
	"encoding/json"
	"testing"
)

func TestBuildA2ARequest_IncludesSkillID(t *testing.T) {
	body, err := buildA2ARequest("message/send", json.RawMessage(`{"text":"hello"}`), "translate")
	if err != nil {
		t.Fatalf("buildA2ARequest failed: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request failed: %v", err)
	}
	params := req["params"].(map[string]any)
	if params["skill_id"] != "translate" {
		t.Fatalf("expected skill_id=translate, got %#v", params["skill_id"])
	}
}

func TestBuildA2ARequest_OmitsSkillIDWhenEmpty(t *testing.T) {
	body, err := buildA2ARequest("message/send", json.RawMessage(`{"text":"hello"}`), "")
	if err != nil {
		t.Fatalf("buildA2ARequest failed: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request failed: %v", err)
	}
	params := req["params"].(map[string]any)
	if _, ok := params["skill_id"]; ok {
		t.Fatalf("expected empty skill_id to be omitted, got %#v", params["skill_id"])
	}
}
