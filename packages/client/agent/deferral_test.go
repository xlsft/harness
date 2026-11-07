package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestBundleSchemasValidate: a direct call of a deferred tool is checked against its schema (loadOnCall), so every
// schema of the bundle must be one the validator can use; one it cannot would let a guessed input through unchecked.
func TestBundleSchemasValidate(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "core", "prompts", "tools", "*", "schema.json"))
	if len(files) == 0 {
		t.Skip("packages/core/prompts is not next to this module")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s jsonschema.Schema
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if _, err := s.Resolve(nil); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestValidateInput(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)
	if p := validateInput(schema, json.RawMessage(`{"id":"a"}`)); p != "" {
		t.Fatalf("valid input: %s", p)
	}
	for _, in := range []string{`{}`, `{"id":1}`, `{"id":"a","x":1}`, `not json`} {
		if validateInput(schema, json.RawMessage(in)) == "" {
			t.Errorf("%s passed", in)
		}
	}
	if validateInput(nil, json.RawMessage(`{"x":1}`)) != "" {
		t.Error("no schema: nothing to check")
	}
}
