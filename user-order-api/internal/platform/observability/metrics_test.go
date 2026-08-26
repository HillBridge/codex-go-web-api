package observability

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNormalizeRouteReplacesDynamicSegments(t *testing.T) {
	for _, testCase := range []struct {
		path string
		want string
	}{
		{path: "/api/v1/orders/42", want: "/api/v1/orders/:id"},
		{path: "/api/v1/orders/42/pay", want: "/api/v1/orders/:id/pay"},
		{path: "/api/v1/orders/42/cancel", want: "/api/v1/orders/:id/cancel"},
		{path: "/api/v1/users/9", want: "/api/v1/users/:id"},
		{path: "/api/v1/auth/login", want: "/api/v1/auth/login"},
		{path: "/unexpected/42", want: "/unknown"},
	} {
		if got := NormalizeRoute(testCase.path); got != testCase.want {
			t.Fatalf("NormalizeRoute(%q) = %q, want %q", testCase.path, got, testCase.want)
		}
	}
}

func TestMiddlewareRecordsFixedRouteAndStatus(t *testing.T) {
	metrics, registry := New(nil, fakeAuditStats{})
	handler := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/orders/42", nil))

	got := gatherMetric(t, registry, "user_order_api_http_requests_total", map[string]string{
		"method": http.MethodPost,
		"route":  "/api/v1/orders/:id",
		"status": "201",
	})
	if got != 1 {
		t.Fatalf("request count = %v, want 1", got)
	}
}

func TestRegistryExportsDatabaseAndAuditStatistics(t *testing.T) {
	_, registry := New(fakeDatabaseStats{open: 4, inUse: 2, idle: 2}, fakeAuditStats{pending: 3, dropped: 1})

	if got := gatherMetric(t, registry, "user_order_api_mysql_open_connections", nil); got != 4 {
		t.Fatalf("open connections = %v, want 4", got)
	}
	if got := gatherMetric(t, registry, "user_order_api_mysql_in_use_connections", nil); got != 2 {
		t.Fatalf("in-use connections = %v, want 2", got)
	}
	if got := gatherMetric(t, registry, "user_order_api_mysql_idle_connections", nil); got != 2 {
		t.Fatalf("idle connections = %v, want 2", got)
	}
	if got := gatherMetric(t, registry, "user_order_api_audit_queue_pending", nil); got != 3 {
		t.Fatalf("audit queue pending = %v, want 3", got)
	}
	if got := gatherMetric(t, registry, "user_order_api_audit_dropped_total", nil); got != 1 {
		t.Fatalf("audit dropped = %v, want 1", got)
	}
}

type fakeAuditStats struct {
	pending int
	dropped uint64
}

func (f fakeAuditStats) Pending() int    { return f.pending }
func (f fakeAuditStats) Dropped() uint64 { return f.dropped }

type fakeDatabaseStats struct {
	open  int
	inUse int
	idle  int
}

func (f fakeDatabaseStats) Stats() sql.DBStats {
	return sql.DBStats{OpenConnections: f.open, InUse: f.inUse, Idle: f.idle}
}

func gatherMetric(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				switch {
				case metric.Counter != nil:
					return metric.Counter.GetValue()
				case metric.Gauge != nil:
					return metric.Gauge.GetValue()
				}
			}
		}
		t.Fatalf("metric %q with labels %#v was not found", name, labels)
	}
	t.Fatalf("metric family %q was not found", name)
	return 0
}

func metricLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	if len(metric.GetLabel()) != len(want) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
