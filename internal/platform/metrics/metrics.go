// Package metrics exposes Prometheus metrics for the API and the worker.
//
// Labels carry only bounded values: chi route patterns (never raw paths),
// HTTP methods and status classes, channels, delivery outcomes, error
// codes, and LLM operations and outcomes. Campaign, delivery, and user
// identifiers, addresses, and message content never become labels.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/provider"
)

// NewRegistry returns a registry with the Go runtime and process
// collectors. Each binary uses its own registry rather than the global
// default one.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Handler serves reg in the Prometheus exposition format.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}

// unmatchedRoute labels requests that matched no route, so scanners
// probing random paths cannot create new series.
const unmatchedRoute = "unmatched"

// HTTP records request counts and latencies.
type HTTP struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewHTTP registers the HTTP metrics on reg.
func NewHTTP(reg prometheus.Registerer) *HTTP {
	m := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by chi route pattern, method, and status class.",
		}, []string{"route", "method", "status_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration by chi route pattern, method, and status class. Server-Sent Event streams are not observed.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status_class"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// knownMethods bounds the method label; anything else is "other".
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// Middleware records every request. It must be installed on the root
// chi router so the full route pattern is known once the request is done.
// Server-Sent Event streams are counted but their duration is not
// observed: it is the viewer's session length, not latency.
func (m *HTTP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := unmatchedRoute
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		method := r.Method
		if !knownMethods[method] {
			method = "other"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		class := strconv.Itoa(status/100) + "xx"
		m.requests.WithLabelValues(route, method, class).Inc()
		if ww.Header().Get("Content-Type") != "text/event-stream" {
			m.duration.WithLabelValues(route, method, class).Observe(time.Since(start).Seconds())
		}
	})
}

// Delivery records worker activity. It implements delivery.Metrics and
// provider.Observer.
type Delivery struct {
	deliveries       *prometheus.CounterVec
	providerDuration *prometheus.HistogramVec
	providerCodes    *prometheus.CounterVec
	retries          *prometheus.CounterVec
	limiterWait      *prometheus.HistogramVec
	outboxPending    prometheus.Gauge
	leasesRecovered  prometheus.Counter
}

var (
	_ delivery.Metrics  = (*Delivery)(nil)
	_ provider.Observer = (*Delivery)(nil)
)

// Delivery outcome label values.
const (
	OutcomeSent     = "sent"
	OutcomeFailed   = "failed"
	OutcomeRetrying = "retrying"
	// OutcomeReleased: shutdown interrupted the attempt before it counted.
	OutcomeReleased = "released"
)

var channels = []content.Channel{content.ChannelSMS, content.ChannelEmail}

// NewDelivery registers the delivery metrics on reg.
func NewDelivery(reg prometheus.Registerer) *Delivery {
	m := &Delivery{
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "deliveries_total",
			Help: "Recorded delivery attempts by channel and outcome (sent, failed, retrying, released).",
		}, []string{"channel", "outcome"}),
		providerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "provider_request_duration_seconds",
			Help:    "Messaging provider request duration by channel.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"channel"}),
		providerCodes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "provider_responses_total",
			Help: "Messaging provider responses by channel and code class (2xx, 3xx, 4xx, 429, 5xx, error).",
		}, []string{"channel", "code_class"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "retries_scheduled_total",
			Help: "Delivery retries scheduled by channel and reason (error code).",
		}, []string{"channel", "reason"}),
		limiterWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rate_limiter_wait_seconds",
			Help:    "Time a delivery attempt waited for the shared per-channel rate limit.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"channel"}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_pending",
			Help: "Unpublished outbox messages, as last counted by this worker (a global value: every worker reports it).",
		}),
		leasesRecovered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "leases_recovered_total",
			Help: "Deliveries returned to retrying or failed after their worker's lease expired.",
		}),
	}
	reg.MustRegister(m.deliveries, m.providerDuration, m.providerCodes, m.retries, m.limiterWait,
		m.outboxPending, m.leasesRecovered)
	// Start the main series at zero, so rates are defined from the first
	// scrape.
	for _, ch := range channels {
		for _, o := range []string{OutcomeSent, OutcomeFailed, OutcomeRetrying, OutcomeReleased} {
			m.deliveries.WithLabelValues(string(ch), o)
		}
	}
	return m
}

// AttemptRecorded implements delivery.Metrics.
func (m *Delivery) AttemptRecorded(c content.Channel, o delivery.Outcome) {
	switch {
	case o.Release:
		m.deliveries.WithLabelValues(string(c), OutcomeReleased).Inc()
	case o.Status == delivery.StatusSent:
		m.deliveries.WithLabelValues(string(c), OutcomeSent).Inc()
	case o.Status == delivery.StatusFailed:
		m.deliveries.WithLabelValues(string(c), OutcomeFailed).Inc()
	case o.Status == delivery.StatusRetrying:
		m.deliveries.WithLabelValues(string(c), OutcomeRetrying).Inc()
		m.retries.WithLabelValues(string(c), o.ErrorCode).Inc()
	}
}

// RateLimitWaited implements delivery.Metrics.
func (m *Delivery) RateLimitWaited(c content.Channel, d time.Duration) {
	m.limiterWait.WithLabelValues(string(c)).Observe(d.Seconds())
}

// LeasesRecovered implements delivery.Metrics.
func (m *Delivery) LeasesRecovered(n int) { m.leasesRecovered.Add(float64(n)) }

// OutboxPending implements delivery.Metrics.
func (m *Delivery) OutboxPending(n int) { m.outboxPending.Set(float64(n)) }

// ProviderRequest implements provider.Observer.
func (m *Delivery) ProviderRequest(c content.Channel, codeClass string, d time.Duration) {
	m.providerDuration.WithLabelValues(string(c)).Observe(d.Seconds())
	m.providerCodes.WithLabelValues(string(c), codeClass).Inc()
}

// LLM records model calls. It implements campaign.LLMMetrics.
type LLM struct {
	requests           *prometheus.CounterVec
	duration           *prometheus.HistogramVec
	validationFailures *prometheus.CounterVec
}

var _ campaign.LLMMetrics = (*LLM)(nil)

// llmInvalidOutcome is the generator's outcome for output that failed
// validation.
const llmInvalidOutcome = "invalid_output"

// NewLLM registers the LLM metrics on reg.
func NewLLM(reg prometheus.Registerer) *LLM {
	m := &LLM{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_requests_total",
			Help: "Model calls (including repair attempts) by operation and outcome.",
		}, []string{"operation", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llm_request_duration_seconds",
			Help:    "Model call duration by operation.",
			Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60, 120},
		}, []string{"operation"}),
		validationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_validation_failures_total",
			Help: "Model outputs rejected by backend validation, by operation.",
		}, []string{"operation"}),
	}
	reg.MustRegister(m.requests, m.duration, m.validationFailures)
	return m
}

// LLMAttempt implements campaign.LLMMetrics.
func (m *LLM) LLMAttempt(operation, outcome string, d time.Duration) {
	m.requests.WithLabelValues(operation, outcome).Inc()
	m.duration.WithLabelValues(operation).Observe(d.Seconds())
	if outcome == llmInvalidOutcome {
		m.validationFailures.WithLabelValues(operation).Inc()
	}
}
