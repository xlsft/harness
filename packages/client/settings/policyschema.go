package settings

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// PolicySchemaJSON is policy.schema.json: the keys an administrator sets on a machine, with their types. The device
// management templates under deploy/mdm are generated from it (internal/policytemplates), and the registry reader takes the
// types of single values from it.
//
//go:embed policy.schema.json
var PolicySchemaJSON []byte

// SchemaProperty is one key of the policy schema. Only what the readers and the template generator use is decoded.
type SchemaProperty struct {
	Title                string                     `json:"title"`
	Description          string                     `json:"description"`
	Type                 string                     `json:"type"`
	Format               string                     `json:"format,omitempty"`
	Enum                 []string                   `json:"enum,omitempty"`
	Items                *SchemaProperty            `json:"items,omitempty"`
	Properties           map[string]*SchemaProperty `json:"properties,omitempty"`
	AdditionalProperties *SchemaProperty            `json:"additionalProperties,omitempty"`
	Examples             []json.RawMessage          `json:"examples,omitempty"`
	Harness              struct {
		// Scope is "machine" (read only from this machine's policy) or "policy" (as in a POLICY document).
		Scope string `json:"scope"`
		// Template is the value the generated templates set; absent keys are documented, not set.
		Template json.RawMessage `json:"template,omitempty"`
	} `json:"x-harness"`
}

// Leaf reports whether the property is a single value (no nested keys of the schema).
func (p *SchemaProperty) Leaf() bool { return p.Type != "object" || len(p.Properties) == 0 }

// Scalar reports whether a registry value of its own can hold it: a boolean, an integer or a string.
func (p *SchemaProperty) Scalar() bool {
	return p.Type == "boolean" || p.Type == "integer" || p.Type == "string"
}

// PolicySchema is the parsed schema.
type PolicySchema struct {
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	Properties  map[string]*SchemaProperty `json:"properties"`
	Harness     struct {
		Domain            string `json:"domain"`
		RegistryKey       string `json:"registryKey"`
		RegistryJSONValue string `json:"registryJsonValue"`
	} `json:"x-harness"`
}

// SchemaKey is a key of the schema with its dotted path ("sandbox.enabled").
type SchemaKey struct {
	Path string
	*SchemaProperty
}

// Keys lists every leaf key in path order.
func (s *PolicySchema) Keys() []SchemaKey {
	var out []SchemaKey
	var walk func(prefix string, props map[string]*SchemaProperty)
	walk = func(prefix string, props map[string]*SchemaProperty) {
		for _, name := range sortedNames(props) {
			p := props[name]
			if p.Leaf() {
				out = append(out, SchemaKey{Path: prefix + name, SchemaProperty: p})
			} else {
				walk(prefix+name+".", p.Properties)
			}
		}
	}
	walk("", s.Properties)
	return out
}

// Lookup finds a leaf key by its dotted path.
func (s *PolicySchema) Lookup(path string) *SchemaProperty {
	props := s.Properties
	parts := strings.Split(path, ".")
	for i, part := range parts {
		p := props[part]
		if p == nil {
			return nil
		}
		if i == len(parts)-1 {
			if p.Leaf() {
				return p
			}
			return nil
		}
		props = p.Properties
	}
	return nil
}

func sortedNames(m map[string]*SchemaProperty) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var policySchema = sync.OnceValue(func() *PolicySchema {
	var s PolicySchema
	if err := json.Unmarshal(PolicySchemaJSON, &s); err != nil {
		panic("settings: policy.schema.json: " + err.Error())
	}
	return &s
})

// Schema returns the parsed policy schema.
func Schema() *PolicySchema { return policySchema() }
