package metrics

import (
	"bytes"
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total number of gateway requests by skill_id and HTTP status.",
	}, []string{"skill_id", "status"})

	RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "End-to-end request latency in seconds.",
		Buckets: []float64{0.005, 0.05, 0.2, 1, 5},
	}, []string{"skill_id"})

	DownstreamDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_downstream_duration_seconds",
		Help:    "Downstream call latency in seconds.",
		Buckets: []float64{0.005, 0.05, 0.2, 1, 5},
	}, []string{"skill_id", "protocol"})

	RateLimitRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_ratelimit_rejected_total",
		Help: "Total number of requests rejected by rate limiter.",
	}, []string{"skill_id"})

	TaskQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_task_queue_depth",
		Help: "Current number of pending async tasks.",
	})

	TaskTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_task_total",
		Help: "Total number of async tasks by final status.",
	}, []string{"status"})

	// A2A Agent 代理指标
	A2AProxyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_a2a_proxy_total",
		Help: "Total number of A2A proxy calls by agent_id and status.",
	}, []string{"agent_id", "status"})

	A2AProxyDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_a2a_proxy_duration_seconds",
		Help:    "A2A proxy call latency in seconds.",
		Buckets: []float64{0.01, 0.1, 0.5, 2, 10},
	}, []string{"agent_id"})

	CircuitBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_circuit_breaker_state",
		Help: "Circuit breaker state (1=active) by agent_id and state.",
	}, []string{"agent_id", "state"})

	DegradedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_degraded_total",
		Help: "Total degradation events by component (ratelimit, circuitbreaker).",
	}, []string{"component"})

	DegradedRecoveryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_degraded_recovery_total",
		Help: "Total recovery events from degraded mode by component.",
	}, []string{"component"})
)

func init() {
	prometheus.MustRegister(
		RequestsTotal,
		RequestDuration,
		DownstreamDuration,
		RateLimitRejected,
		TaskQueueDepth,
		TaskTotal,
		A2AProxyTotal,
		A2AProxyDuration,
		CircuitBreakerState,
		DegradedTotal,
		DegradedRecoveryTotal,
	)
}

// hertzResponseWriter adapts a bytes.Buffer + status code to http.ResponseWriter
// so that promhttp.Handler can write into it.
type hertzResponseWriter struct {
	buf    bytes.Buffer
	header http.Header
	status int
}

func (w *hertzResponseWriter) Header() http.Header        { return w.header }
func (w *hertzResponseWriter) WriteHeader(code int)       { w.status = code }
func (w *hertzResponseWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }

// MetricsHandler is a Hertz-native handler for GET /metrics.
func MetricsHandler() app.HandlerFunc {
	h := promhttp.Handler()
	return func(_ context.Context, c *app.RequestContext) {
		w := &hertzResponseWriter{header: make(http.Header), status: http.StatusOK}
		// promhttp needs a minimal *http.Request (method + URL)
		r, _ := http.NewRequest(http.MethodGet, "/metrics", nil)
		h.ServeHTTP(w, r)
		for k, vals := range w.header {
			for _, v := range vals {
				c.Response.Header.Set(k, v)
			}
		}
		c.Response.SetStatusCode(w.status)
		c.Response.SetBody(w.buf.Bytes())
	}
}
