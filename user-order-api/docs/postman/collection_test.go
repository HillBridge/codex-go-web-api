package postman

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCollectionKeepsGeneratedEmailStable(t *testing.T) {
	data, err := os.ReadFile("user-order-api.postman_collection.json")
	if err != nil {
		t.Fatal(err)
	}

	var collection struct {
		Variables []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"variable"`
		Events []struct {
			Script struct {
				Exec []string `json:"exec"`
			} `json:"script"`
		} `json:"event"`
	}
	if err := json.Unmarshal(data, &collection); err != nil {
		t.Fatal(err)
	}

	for _, variable := range collection.Variables {
		if variable.Key == "email" && strings.Contains(variable.Value, "{{$guid}}") {
			t.Fatal("email must not generate a different value for every request")
		}
	}

	for _, event := range collection.Events {
		if strings.Contains(strings.Join(event.Script.Exec, "\n"), "pm.collectionVariables.set('email'") {
			return
		}
	}
	t.Fatal("collection must initialize and persist one email address")
}
