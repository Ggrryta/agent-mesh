package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// ── request_id ─────────────────────────────────────────────────────────

func TestRequestID_GeneratedWhenAbsent(t *testing.T) {
	var seenID string
	h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFromContext(r.Context())
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if seenID == "" {
		t.Fatal("expected generated id in context")
	}
	if len(seenID) != 32 {
		t.Fatalf("expected 32-hex id, got %q", seenID)
	}
	if got := rr.Header().Get(RequestIDHeader); got != seenID {
		t.Fatalf("response header mismatch: header=%q ctx=%q", got, seenID)
	}
}

func TestRequestID_ReusesIncomingHeader(t *testing.T) {
	want := "incoming-trace-id-from-lb"
	var seenID string
	h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, want)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if seenID != want {
		t.Fatalf("want %q, got %q", want, seenID)
	}
	if got := rr.Header().Get(RequestIDHeader); got != want {
		t.Fatalf("response header not echoed: %q", got)
	}
}

func TestRequestID_RejectsOverlongIncoming(t *testing.T) {
	// 超长 (>128) 的客户端传入值被当成非法，换成我们生成的。
	long := strings.Repeat("x", 200)
	var seenID string
	h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenID = RequestIDFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, long)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seenID == long || len(seenID) != 32 {
		t.Fatalf("overlong should be replaced, got %q", seenID)
	}
}

// ── access_log ─────────────────────────────────────────────────────────

// newObservedLogger 返回一个可捕获日志条目的 logger，便于断言字段。
func newObservedLogger(t *testing.T, minLevel zap.AtomicLevel) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(minLevel.Level())
	return zap.New(core), logs
}

func TestAccessLog_BasicFields(t *testing.T) {
	lvl := zap.NewAtomicLevelAt(zap.DebugLevel)
	log, logs := newObservedLogger(t, lvl)

	h := WithRequestID(AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/x", nil))

	entries := logs.FilterMessage("access").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 access log, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["method"] != "POST" || m["path"] != "/v1/x" {
		t.Fatalf("method/path: %+v", m)
	}
	if m["status"].(int64) != int64(http.StatusTeapot) {
		t.Fatalf("status: %v", m["status"])
	}
	if m["bytes"].(int64) == 0 {
		t.Fatalf("bytes should be >0")
	}
	if m["request_id"] == "" {
		t.Fatalf("request_id missing")
	}
}

func TestAccessLog_ProbePathDemoted(t *testing.T) {
	// Probe 路径在 info 级别的 logger 下不应被记录。
	lvl := zap.NewAtomicLevelAt(zap.InfoLevel)
	log, logs := newObservedLogger(t, lvl)

	h := WithRequestID(AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if len(logs.All()) != 0 {
		t.Fatalf("probe path should be demoted below info, got: %+v", logs.All())
	}
}

func TestAccessLog_IncludesClaims(t *testing.T) {
	lvl := zap.NewAtomicLevelAt(zap.DebugLevel)
	log, logs := newObservedLogger(t, lvl)

	// 外层：request_id；内层：access_log。
	// 我们在 access_log 之前把 claims 塞进 context，模拟 auth middleware。
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	withClaims := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyClaims, newClaimsForTest(77, "alice-dev"))
		// access_log 已经开始计时；内层 handler 只需要看到带 claims 的 ctx。
		AccessLog(log)(inner).ServeHTTP(w, r.WithContext(ctx))
	})
	h := WithRequestID(withClaims)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	m := logs.FilterMessage("access").All()[0].ContextMap()
	if m["uid"].(int64) != 77 {
		t.Fatalf("uid: %v", m["uid"])
	}
	if m["agent_id"] != "alice-dev" {
		t.Fatalf("agent_id: %v", m["agent_id"])
	}
}

// ── recover ────────────────────────────────────────────────────────────

func TestRecover_Returns500AndLogs(t *testing.T) {
	lvl := zap.NewAtomicLevelAt(zap.DebugLevel)
	log, logs := newObservedLogger(t, lvl)

	h := WithRequestID(Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
	// 响应 body 是标准 ErrorBody
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not json: %s", rr.Body.String())
	}
	if int(body["code"].(float64)) == 0 {
		t.Fatalf("code missing: %+v", body)
	}

	entries := logs.FilterMessage("panic recovered").All()
	if len(entries) != 1 {
		t.Fatalf("expected panic log, got %d entries", len(entries))
	}
	m := entries[0].ContextMap()
	if m["path"] != "/" {
		t.Fatalf("path: %v", m["path"])
	}
	if m["request_id"] == "" {
		t.Fatal("request_id missing from panic log")
	}
}

func TestRecover_LetsErrAbortHandlerPropagate(t *testing.T) {
	log := zap.NewNop()
	h := Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("ErrAbortHandler should propagate, got %v", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
