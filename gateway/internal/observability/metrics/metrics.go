package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ─── HTTP 请求指标 ───────────────────────────────────────────────

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "Total HTTP requests by method, path pattern, and status code.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "mesh",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method", "path"})
)

// ─── Task 指标 ───────────────────────────────────────────────────

var (
	TasksCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "task",
		Name:      "created_total",
		Help:      "Total tasks created.",
	})

	TaskTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "task",
		Name:      "transitions_total",
		Help:      "Task state transitions by target state.",
	}, []string{"to_state"})

	TaskMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "task",
		Name:      "messages_total",
		Help:      "Total task messages appended.",
	})

	TaskArtifactsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "task",
		Name:      "artifacts_total",
		Help:      "Total task artifacts appended.",
	})
)

// ─── Inbox 指标 ──────────────────────────────────────────────────

var (
	InboxEnqueueTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "inbox",
		Name:      "enqueue_total",
		Help:      "Total inbox events enqueued by kind.",
	}, []string{"kind"})

	InboxPullTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "inbox",
		Name:      "pull_total",
		Help:      "Total inbox pull requests.",
	})

	InboxPullEventsReturned = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "mesh",
		Subsystem: "inbox",
		Name:      "pull_events_returned",
		Help:      "Number of events returned per pull request.",
		Buckets:   []float64{0, 1, 5, 10, 25, 50, 100},
	})

	InboxPushSuccessTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "inbox",
		Name:      "push_success_total",
		Help:      "Total successful push deliveries.",
	})

	InboxPushFailTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "inbox",
		Name:      "push_fail_total",
		Help:      "Total failed push deliveries.",
	})
)

// ─── Feed 指标 ───────────────────────────────────────────────────

var (
	FeedPublishTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "feed",
		Name:      "publish_total",
		Help:      "Total feed events published.",
	})

	FeedActiveSubscribers = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "mesh",
		Subsystem: "feed",
		Name:      "active_subscribers",
		Help:      "Current number of active WebSocket subscribers.",
	})
)

// ─── Outbox 指标 ──────────────────────────────────────────────────

var (
	OutboxDeadLetterTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "outbox",
		Name:      "dead_letter_total",
		Help:      "Total events moved to dead letter queue.",
	})
)

// ─── Agent 指标 ──────────────────────────────────────────────────

var (
	AgentOnlineTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "mesh",
		Subsystem: "agent",
		Name:      "cache_size",
		Help:      "Number of agents in the in-memory cache.",
	})

	AgentHeartbeatTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "mesh",
		Subsystem: "agent",
		Name:      "heartbeat_total",
		Help:      "Total agent heartbeats received.",
	})
)
