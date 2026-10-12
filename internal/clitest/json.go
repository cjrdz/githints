package clitest

import (
	"encoding/json"
	"os"
	"testing"
)

// ReadJSON reads path and unmarshals it as JSON.
func ReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, data)
	}
	return doc
}
