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
