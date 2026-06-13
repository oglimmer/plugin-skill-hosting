// Package metrics owns the Prometheus registry and exposes the counters,
// middleware, and /metrics handler used across the rest of the backend.
package metrics

import (
	"bufio"
	"database/sql"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry is a process-local registry. We don't use the default global
// registry so /metrics output is deterministic regardless of which third-party
// libraries the binary happens to import.
var registry = prometheus.NewRegistry()

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_http_requests_total",
			Help: "Total HTTP requests by method, route pattern, and status class.",
		},
		[]string{"method", "route", "code"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "psh_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)

	HTTPRequestsInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "psh_http_requests_in_flight",
			Help: "Number of HTTP requests currently being served.",
		},
	)

	LoginsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_logins_total",
			Help: "Login attempts by mode (password|oidc) and result (success|failure).",
		},
		[]string{"mode", "result"},
	)

	PluginMutationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_plugin_mutations_total",
			Help: "Plugin mutation count by action (create|update|delete|restore) and result.",
		},
		[]string{"action", "result"},
	)

	SkillMutationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_skill_mutations_total",
			Help: "Skill mutation count by action (create|update|delete|restore|revert) and result.",
		},
		[]string{"action", "result"},
	)

	SkillFileMutationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_skill_file_mutations_total",
			Help: "Skill-file mutation count by action (upsert|delete) and result.",
		},
		[]string{"action", "result"},
	)

	GitMaterializeDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "psh_git_materialize_duration_seconds",
			Help:    "Time to regenerate a plugin's git working tree and force-push to its bare repo.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		},
	)

	GitMaterializeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_git_materialize_total",
			Help: "Plugin git-materialize attempts by result (success|error).",
		},
		[]string{"result"},
	)

	ClaudeValidationDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "psh_claude_validation_duration_seconds",
			Help:    "Latency of /api/skills/validate calls to the Claude API.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 90},
		},
	)

	ClaudeValidationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_claude_validation_total",
			Help: "Skill validation calls by result (success|error).",
		},
		[]string{"result"},
	)

	ClaudeFindingFixDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "psh_claude_finding_fix_duration_seconds",
			Help:    "Latency of /api/skills/finding-fix calls to the Claude API.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 90},
		},
	)

	ClaudeFindingFixTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_claude_finding_fix_total",
			Help: "Per-finding fix calls by result (success|error).",
		},
		[]string{"result"},
	)

	MCPToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_mcp_tool_calls_total",
			Help: "MCP tool invocations by tool name and result (success|error).",
		},
		[]string{"tool", "result"},
	)

	MCPToolCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "psh_mcp_tool_call_duration_seconds",
			Help:    "MCP tool invocation latency by tool name.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tool"},
	)

	SkillAuditTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_skill_audit_total",
			Help: "Per-skill security audit calls by result (success|error).",
		},
		[]string{"result"},
	)

	SkillAuditRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "psh_skill_audit_runs_total",
			Help: "Completed audit sweeps by trigger (scheduled|manual).",
		},
		[]string{"trigger"},
	)

	// SkillAuditFlaggedSkills is the metrics-side analog of the audit alert
	// email: the number of skills whose latest audit score reached or exceeded
	// the alert threshold, as of the last completed sweep. Alert on `> 0` to
	// learn that risky skills are live, independent of SMTP being configured.
	SkillAuditFlaggedSkills = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "psh_skill_audit_flagged_skills",
			Help: "Skills at or above the audit alert threshold as of the last completed sweep.",
		},
	)

	// SkillAuditRiskScore is the latest audit risk score (0-100) per skill,
	// giving the per-skill detail the alert email lists. Re-populated each
	// sweep (stale series for deleted/renamed skills are cleared via Reset), so
	// monitoring can alert on specific skills and apply its own thresholds.
	SkillAuditRiskScore = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "psh_skill_audit_risk_score",
			Help: "Latest security-audit risk score (0-100) per skill, by plugin, skill, and risk level.",
		},
		[]string{"plugin", "skill", "level"},
	)

	// SkillAuditLastRunTimestamp is the Unix time of the last completed sweep,
	// so a silently-stalled audit goroutine is detectable (e.g. alert on
	// `time() - metric > 2 * interval`) — a stale gauge is as visible as a
	// missing alert email.
	SkillAuditLastRunTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "psh_skill_audit_last_run_timestamp_seconds",
			Help: "Unix timestamp of the last completed skill audit sweep.",
		},
	)
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequestsTotal,
		HTTPRequestDuration,
		HTTPRequestsInFlight,
		LoginsTotal,
		PluginMutationsTotal,
		SkillMutationsTotal,
		SkillFileMutationsTotal,
		GitMaterializeDuration,
		GitMaterializeTotal,
		ClaudeValidationDuration,
		ClaudeValidationTotal,
		ClaudeFindingFixDuration,
		ClaudeFindingFixTotal,
		MCPToolCallsTotal,
		MCPToolCallDuration,
		SkillAuditTotal,
		SkillAuditRunsTotal,
		SkillAuditFlaggedSkills,
		SkillAuditRiskScore,
		SkillAuditLastRunTimestamp,
	)
}

// RegisterDBStats wires the *sql.DB pool stats into the registry.
// Called from main once the DB handle is available.
func RegisterDBStats(db *sql.DB) {
	registry.MustRegister(collectors.NewDBStatsCollector(db, "marketplace"))
}

// Handler returns the /metrics http.Handler. When token is non-empty, the
// handler requires Authorization: Bearer <token>; otherwise it is open
// (relies on network-level controls — the public ingress for this app does
// not route /metrics, so unauthenticated access is only reachable in-cluster).
func Handler(token string) http.Handler {
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Registry:      registry,
		ErrorHandling: promhttp.ContinueOnError,
	})
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code written
// by the downstream handler so the metrics middleware can label by code.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it implements http.Flusher.
// gitkit's git-upload-pack handler type-asserts the response writer to
// { Flush(); Write([]byte) (int, error) } and panics if Flush is missing.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when it implements http.Hijacker,
// for handlers that need to take over the connection (e.g. websockets).
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// HTTPMiddleware records request count, duration, and in-flight gauge. It
// uses the chi route pattern as the "route" label so cardinality stays
// bounded (URL params don't end up as distinct labels). /metrics itself is
// excluded so scrape traffic doesn't contaminate the histogram.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		HTTPRequestsInFlight.Inc()
		defer HTTPRequestsInFlight.Dec()
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start).Seconds()

		route := "<unmatched>"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		method := r.Method
		code := strconv.Itoa(rec.status)
		HTTPRequestsTotal.WithLabelValues(method, route, code).Inc()
		HTTPRequestDuration.WithLabelValues(method, route).Observe(elapsed)
	})
}

// ResultLabel maps an error to "success"/"error" for counter labels.
func ResultLabel(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
}
