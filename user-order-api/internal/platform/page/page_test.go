package page

import (
	"encoding/json"
	"testing"
)

func TestResultOmitsNextCursorWhenNoFurtherPage(t *testing.T) {
	encoded, err := json.Marshal(Result[int]{Items: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"items":[1]}` {
		t.Fatalf("JSON = %s, want items without nextCursor", got)
	}
}
