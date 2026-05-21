package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestLiveness_AlwaysOK(t *testing.T) {
	p := New()
	rr := httptest.NewRecorder()
	p.LivenessHandler()(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := decodeBody(t, rr)
	if body["status"] != "alive" {
		t.Fatalf("want status=alive, got %v", body["status"])
	}
}

func TestStartup_FlipsAfterMarkStarted(t *testing.T) {
	p := New()

	rr1 := httptest.NewRecorder()
	p.StartupHandler()(rr1, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if rr1.Code != http.StatusServiceUnavailable {
		t.Fatalf("before MarkStarted want 503, got %d", rr1.Code)
	}

	p.MarkStarted()

	rr2 := httptest.NewRecorder()
	p.StartupHandler()(rr2, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("after MarkStarted want 200, got %d", rr2.Code)
	}
}

func TestReadiness_NoChecks_OK(t *testing.T) {
	p := New()
	rr := httptest.NewRecorder()
	p.ReadinessHandler()(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := decodeBody(t, rr)
	if body["status"] != "ready" {
		t.Fatalf("want ready, got %v", body["status"])
	}
}

func TestReadiness_FailingChecker(t *testing.T) {
	p := New()
	p.AddReadinessCheck("mysql", func(ctx context.Context) error {
		return errors.New("connection refused")
	})
	p.AddReadinessCheck("redis", func(ctx context.Context) error {
		return nil
	})

	rr := httptest.NewRecorder()
	p.ReadinessHandler()(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
	body := decodeBody(t, rr)
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks should be an object, got %T", body["checks"])
	}
	if checks["mysql"] != "connection refused" {
		t.Fatalf("want mysql=connection refused, got %v", checks["mysql"])
	}
	if checks["redis"] != "ok" {
		t.Fatalf("want redis=ok, got %v", checks["redis"])
	}
}

func TestReadiness_Draining_Returns503(t *testing.T) {
	p := New()
	p.AddReadinessCheck("always-ok", func(ctx context.Context) error { return nil })
	p.BeginDraining()
	if !p.IsDraining() {
		t.Fatal("IsDraining should be true after BeginDraining")
	}

	rr := httptest.NewRecorder()
	p.ReadinessHandler()(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining: want 503, got %d", rr.Code)
	}
	body := decodeBody(t, rr)
	if body["status"] != "draining" {
		t.Fatalf("want status=draining, got %v", body["status"])
	}
}
