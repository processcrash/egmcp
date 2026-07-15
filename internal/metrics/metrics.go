// Package metrics exposes Prometheus collectors used by the
// platform. The collectors are registered on a private registry
// (not the default) so /metrics only returns egmcp-specific series.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"net/http"
)

// Registry holds the egmcp-specific collectors. We use a private
// registry to keep the metric surface tidy — applications that
// also import client_golang elsewhere won't have their series show
// up under /metrics.
type Registry struct {
	Reg *prometheus.Registry

	MCPCallsTotal     *prometheus.CounterVec
	MCPCallDuration   *prometheus.HistogramVec
	HTTPRequestsTotal *prometheus.CounterVec
	ActiveInstances   prometheus.Gauge
}

// New constructs a fresh Registry with all collectors wired up.
func New() *Registry {
	r := &Registry{
		Reg: prometheus.NewRegistry(),
	}
	r.MCPCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "egmcp_mcp_calls_total",
		Help: "Total MCP tool calls, labelled by instance / connector / tool / status.",
	}, []string{"instance", "connector", "tool", "status"})
	r.MCPCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "egmcp_mcp_call_duration_seconds",
		Help:    "MCP tool call latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"instance", "connector", "tool"})
	r.HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "egmcp_http_requests_total",
		Help: "Total HTTP requests served, labelled by route class and status.",
	}, []string{"path", "method", "status"})
	r.ActiveInstances = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "egmcp_active_instances",
		Help: "Number of currently loaded MCP instances.",
	})

	r.Reg.MustRegister(
		r.MCPCallsTotal,
		r.MCPCallDuration,
		r.HTTPRequestsTotal,
		r.ActiveInstances,
	)
	return r
}

// Handler returns an http.Handler that serves /metrics in the
// Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.Reg, promhttp.HandlerOpts{})
}

// ObserveMCPCall records one MCP tool call. Safe for concurrent use.
func (r *Registry) ObserveMCPCall(instance, connector, tool, status string, seconds float64) {
	r.MCPCallsTotal.WithLabelValues(instance, connector, tool, status).Inc()
	r.MCPCallDuration.WithLabelValues(instance, connector, tool).Observe(seconds)
}

// ObserveHTTP records one HTTP request served.
func (r *Registry) ObserveHTTP(path, method string, status int) {
	r.HTTPRequestsTotal.WithLabelValues(path, method, statusClass(status)).Inc()
}

// SetActiveInstances updates the gauge.
func (r *Registry) SetActiveInstances(n int) { r.ActiveInstances.Set(float64(n)) }

// statusClass collapses status codes to 1xx/2xx/3xx/4xx/5xx.
func statusClass(code int) string {
	switch {
	case code < 200:
		return "1xx"
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}