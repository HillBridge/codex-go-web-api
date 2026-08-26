package observability

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type DatabaseStats interface {
	Stats() sql.DBStats
}

type AuditStats interface {
	Pending() int
	Dropped() uint64
}

type Metrics struct {
	requests  *prometheus.CounterVec
	durations *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	registry  *prometheus.Registry
}

func New(db DatabaseStats, audit AuditStats) (*Metrics, *prometheus.Registry) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	metrics := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "user_order_api",
			Name:      "http_requests_total",
			Help:      "Total HTTP requests served.",
		}, []string{"method", "route", "status"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "user_order_api",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
		}, []string{"method", "route", "status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "user_order_api",
			Name:      "http_requests_in_flight",
			Help:      "Current HTTP requests being processed.",
		}),
		registry: registry,
	}
	registry.MustRegister(metrics.requests, metrics.durations, metrics.inFlight)

	if db != nil {
		registry.MustRegister(
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "user_order_api", Name: "mysql_open_connections", Help: "Current open MySQL connections."}, func() float64 { return float64(db.Stats().OpenConnections) }),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "user_order_api", Name: "mysql_in_use_connections", Help: "Current in-use MySQL connections."}, func() float64 { return float64(db.Stats().InUse) }),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "user_order_api", Name: "mysql_idle_connections", Help: "Current idle MySQL connections."}, func() float64 { return float64(db.Stats().Idle) }),
		)
	}

	registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "user_order_api", Name: "audit_queue_pending", Help: "Current pending audit records."}, func() float64 {
			if audit == nil {
				return 0
			}
			return float64(audit.Pending())
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "user_order_api", Name: "audit_dropped_total", Help: "Total best-effort audit records dropped."}, func() float64 {
			if audit == nil {
				return 0
			}
			return float64(audit.Dropped())
		}),
	)

	return metrics, registry
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		next.ServeHTTP(recorder, r)

		labels := []string{r.Method, NormalizeRoute(r.URL.Path), strconv.Itoa(recorder.statusCode())}
		m.requests.WithLabelValues(labels...).Inc()
		m.durations.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
	})
}

func NormalizeRoute(path string) string {
	fixedRoutes := map[string]struct{}{
		"/healthz":              {},
		"/readyz":               {},
		"/metrics":              {},
		"/api/v1/health":        {},
		"/api/v1/auth/register": {},
		"/api/v1/auth/login":    {},
		"/api/v1/auth/refresh":  {},
		"/api/v1/auth/logout":   {},
		"/api/v1/auth/me":       {},
		"/api/v1/users":         {},
		"/api/v1/orders":        {},
	}
	if _, ok := fixedRoutes[path]; ok {
		return path
	}

	if strings.HasPrefix(path, "/api/v1/users/") && len(strings.Split(strings.TrimPrefix(path, "/api/v1/users/"), "/")) == 1 {
		return "/api/v1/users/:id"
	}

	if strings.HasPrefix(path, "/api/v1/orders/") {
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/orders/"), "/")
		switch {
		case len(segments) == 1:
			return "/api/v1/orders/:id"
		case len(segments) == 2 && segments[1] == "pay":
			return "/api/v1/orders/:id/pay"
		case len(segments) == 2 && segments[1] == "cancel":
			return "/api/v1/orders/:id/cancel"
		}
	}

	return "/unknown"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(value)
}

func (w *statusRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
