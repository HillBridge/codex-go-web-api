package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestHostRunScriptsStartOnlyMySQL(t *testing.T) {
	for _, path := range []string{"start-local.sh", "start-stage7.sh"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "docker compose up -d mysql") {
			t.Fatalf("%s must start only mysql before it runs a host API process", path)
		}
	}
}

func TestComposeIncludesTracingAndAlerting(t *testing.T) {
	compose := readProjectFile(t, "../compose.yaml")
	prometheus := readProjectFile(t, "../docker/prometheus/prometheus.yml")
	alerts := readProjectFile(t, "../docker/prometheus/alerts.yml")

	for _, expected := range []string{"jaeger:", "alertmanager:", "OTEL_EXPORTER_OTLP_ENDPOINT: jaeger:4317", "16686:16686", "9093:9093"} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose.yaml does not contain %q", expected)
		}
	}
	for _, expected := range []string{"rule_files:", "alerts.yml", "alertmanager:9093"} {
		if !strings.Contains(prometheus, expected) {
			t.Fatalf("prometheus.yml does not contain %q", expected)
		}
	}
	for _, expected := range []string{"alert: UserOrderAPIUnavailable", "alert: UserOrderAPI5xxResponses", "alert: AuditEventsDropped", "alert: MySQLConnectionPoolSaturated"} {
		if !strings.Contains(alerts, expected) {
			t.Fatalf("alerts.yml does not contain %q", expected)
		}
	}
	if strings.Contains(compose, "down -v") || strings.Contains(prometheus, "down -v") || strings.Contains(alerts, "down -v") {
		t.Fatal("observability configuration must not include destructive down -v command")
	}
}

func readProjectFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestObservabilityDocumentationIncludesTracingAndAlerts(t *testing.T) {
	readme := readProjectFile(t, "../README.md")
	documentation := readProjectFile(t, "../docs/local-docker-prometheus.md")
	content := readme + "\n" + documentation
	for _, expected := range []string{
		"http://localhost:16686",
		"http://localhost:9093",
		"trace_id",
		"UserOrderAPIUnavailable",
		"docker compose down -v",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("observability documentation does not contain %q", expected)
		}
	}
}
