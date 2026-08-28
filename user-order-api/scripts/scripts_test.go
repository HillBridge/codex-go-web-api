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

func TestComposeIncludesRedis(t *testing.T) {
	compose := readProjectFile(t, "../compose.yaml")
	for _, expected := range []string{"redis:", "image: redis:7.4-alpine", "REDIS_ADDR:", "REDIS_ENVIRONMENT:", "redis-cli", "redis_data", "condition: service_healthy"} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose.yaml does not contain %q", expected)
		}
	}
	if strings.Contains(compose, "6379:6379") {
		t.Fatal("Redis must remain internal to the Compose network")
	}
}

func TestComposeIncludesRabbitMQ(t *testing.T) {
	compose := readProjectFile(t, "../compose.yaml")
	for _, expected := range []string{"rabbitmq:", "rabbitmq:4-management-alpine", "5672:5672", "15672:15672", "rabbitmq-diagnostics", "rabbitmq_data", "RABBITMQ_URL:", "condition: service_healthy"} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose.yaml does not contain %q", expected)
		}
	}
	if strings.Contains(compose, "down -v") {
		t.Fatal("compose.yaml must not include destructive volume commands")
	}
}

func TestRabbitMQOutboxSmokeScript(t *testing.T) {
	content := readProjectFile(t, "rabbitmq-outbox-smoke.sh")
	for _, expected := range []string{"rabbitmq-diagnostics", "outbox_events", "inbox_events", "/healthz", "只读"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("rabbitmq-outbox-smoke.sh does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"docker compose down -v", "DROP DATABASE", "FLUSHDB", "docker volume rm"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("rabbitmq-outbox-smoke.sh contains forbidden operation %q", forbidden)
		}
	}
}

func TestRedisRateLimitSmokeScript(t *testing.T) {
	content := readProjectFile(t, "redis-rate-limit-smoke.sh")
	for _, expected := range []string{"REDIS_SMOKE_LIMIT", "REDIS_ENVIRONMENT", "REDIS_ADDR=redis:6379", "docker rm -f", "8888", "8889", "429"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("redis-rate-limit-smoke.sh does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"docker compose down -v", "FLUSHDB", "DROP DATABASE", "docker volume rm"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("redis-rate-limit-smoke.sh contains forbidden operation %q", forbidden)
		}
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

func TestLoadTestScriptIsReadOnly(t *testing.T) {
	content := readProjectFile(t, "load-test.js")
	for _, expected := range []string{"http.get", "/api/v1/health", "constant-arrival-rate"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("load-test.js does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"http.post", "http.put", "http.patch", "http.del", "/auth/register", "/orders"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("load-test.js contains mutating operation %q", forbidden)
		}
	}
}

func TestMultiInstanceSmokeScriptRequiresExplicitCredentials(t *testing.T) {
	content := readProjectFile(t, "multi-instance-smoke.sh")
	for _, expected := range []string{
		"MULTI_INSTANCE_EMAIL",
		"MULTI_INSTANCE_PASSWORD",
		"MULTI_INSTANCE_PORT:-8889",
		"/api/v1/auth/login",
		"/api/v1/auth/refresh",
		"/api/v1/auth/me",
		"docker rm -f",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("multi-instance-smoke.sh does not contain %q", expected)
		}
	}
	if strings.Contains(content, "docker compose down -v") {
		t.Fatal("multi-instance-smoke.sh must not delete data volumes")
	}
}
