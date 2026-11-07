// Package policytemplates generates the device management templates under deploy/mdm from the policy schema
// (settings/policy.schema.json), so the two can't drift: a macOS configuration profile, the preference file Intune takes,
// a Jamf Pro custom schema, a Windows administrative template (ADMX and ADML), a .reg example and the key reference.
// scripts/policy-templates.sh writes them; the package's test fails when the committed files differ.
package policytemplates

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/settings"
)

// Header says, in every file that can carry a comment, where it came from.
const Header = "Generated from packages/client/settings/policy.schema.json by scripts/policy-templates.sh. Don't edit it here: change the schema and run the script."

// Generate returns every template by its path under deploy/mdm.
func Generate(s *settings.PolicySchema) (map[string][]byte, error) {
	tmpl, err := templateValues(s)
	if err != nil {
		return nil, err
	}
	jamf, err := jamfSchema(s)
	if err != nil {
		return nil, err
	}
	reg, err := regFile(s)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"macos/com.xlsft.harness.mobileconfig": []byte(mobileconfig(s, tmpl)),
		"macos/com.xlsft.harness.plist":        []byte(preferenceFile(tmpl)),
		"macos/jamf-custom-schema.json":        jamf,
		"windows/harness.admx":                 []byte(admx(s)),
		"windows/en-US/harness.adml":           []byte(adml(s)),
		"windows/harness.reg":                  []byte(reg),
		"keys.md":                              []byte(keysMarkdown(s)),
	}, nil
}

func decode(raw json.RawMessage) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}

// templateValues is the dictionary the templates set: every key with an x-harness template value, nested by its path.
func templateValues(s *settings.PolicySchema) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range s.Keys() {
		if len(k.Harness.Template) == 0 {
			continue
		}
		v, err := decode(k.Harness.Template)
		if err != nil {
			return nil, fmt.Errorf("%s: template: %w", k.Path, err)
		}
		setPath(out, strings.Split(k.Path, "."), v)
	}
	return out, nil
}

func setPath(doc map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := doc[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[k] = next
		}
		doc = next
	}
	doc[path[len(path)-1]] = v
}

// uuid is a stable UUID for a name, so regenerating changes nothing. Its form is that of a version 5 UUID.
func uuid(name string) string {
	h := sha256.Sum256([]byte("xlsft-harness-mdm:" + name))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16]))
}

func mobileconfig(s *settings.PolicySchema, tmpl map[string]any) string {
	domain := s.Harness.Domain
	one := json.Number("1")
	profile := map[string]any{
		"PayloadContent": []any{map[string]any{
			"PayloadType":        "com.apple.ManagedClient.preferences",
			"PayloadVersion":     one,
			"PayloadIdentifier":  domain + ".profile.preferences",
			"PayloadUUID":        uuid("preferences"),
			"PayloadDisplayName": "xlsft`s harness settings",
			"PayloadEnabled":     true,
			"PayloadContent": map[string]any{
				domain: map[string]any{
					"Forced": []any{map[string]any{"mcx_preference_settings": tmpl}},
				},
			},
		}},
		"PayloadDescription":       "Server, updates and managed policy of xlsft`s harness. Keys: deploy/mdm/keys.md.",
		"PayloadDisplayName":       "xlsft`s harness",
		"PayloadIdentifier":        domain + ".profile",
		"PayloadOrganization":      "Example Organization",
		"PayloadRemovalDisallowed": true,
		"PayloadScope":             "System",
		"PayloadType":              "Configuration",
		"PayloadUUID":              uuid("profile"),
		"PayloadVersion":           one,
	}
	doc := plistDocument(profile)
	return strings.Replace(doc, "<plist version=\"1.0\">\n", "<!-- "+Header+" Copy it, set serverUrl and the keys you want (keys.md), and give PayloadUUID and PayloadIdentifier values of your own. -->\n<plist version=\"1.0\">\n", 1)
}

func preferenceFile(tmpl map[string]any) string {
	doc := plistDocument(tmpl)
	return strings.Replace(doc, "<plist version=\"1.0\">\n", "<!-- "+Header+" The preference file for Intune (macOS, Preference file), or any MDM that takes plain keys. -->\n<plist version=\"1.0\">\n", 1)
}

// jamfSchema is the custom schema of Jamf Pro's Application & Custom Settings: the JSON Schema subset its form builder
// knows. A key that is a free-form dictionary (enabledPlugins) can't be a form field there; the preference file sets it.
func jamfSchema(s *settings.PolicySchema) ([]byte, error) {
	var conv func(p *settings.SchemaProperty) (map[string]any, bool, error)
	conv = func(p *settings.SchemaProperty) (map[string]any, bool, error) {
		if p.AdditionalProperties != nil {
			return nil, false, nil
		}
		out := map[string]any{"type": p.Type}
		if p.Title != "" {
			out["title"] = p.Title
		}
		if p.Description != "" {
			out["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			out["enum"] = p.Enum
		}
		if len(p.Harness.Template) > 0 {
			v, err := decode(p.Harness.Template)
			if err != nil {
				return nil, false, err
			}
			out["default"] = v
		}
		if p.Items != nil {
			items, ok, err := conv(p.Items)
			if err != nil || !ok {
				return nil, ok, err
			}
			out["items"] = items
		}
		if len(p.Properties) > 0 {
			props := map[string]any{}
			for name, c := range p.Properties {
				cv, ok, err := conv(c)
				if err != nil {
					return nil, false, fmt.Errorf("%s: %w", name, err)
				}
				if ok {
					props[name] = cv
				}
			}
			out["properties"] = props
		}
		return out, true, nil
	}
	props := map[string]any{}
	for name, p := range s.Properties {
		v, ok, err := conv(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if ok {
			props[name] = v
		}
	}
	doc := map[string]any{
		"title":       "xlsft`s harness (" + s.Harness.Domain + ")",
		"description": "Server, updates and managed policy of xlsft`s harness. " + Header,
		"properties":  props,
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Windows: one policy per single value, under one category per object of the schema, and one for the Settings JSON that
// holds the lists and dictionaries.

func policyName(path string) string { return strings.ReplaceAll(path, ".", "_") }

func categoryOf(path string) string {
	if i := strings.Index(path, "."); i > 0 {
		return "harness_" + path[:i]
	}
	return "harness"
}

// admxKind says how a key is written to the registry, "" when only the Settings JSON can hold it.
func admxKind(p *settings.SchemaProperty) string {
	switch {
	case p.Type == "boolean":
		return "boolean"
	case p.Type == "string" && len(p.Enum) == 1:
		return "flag"
	case p.Type == "string" && len(p.Enum) > 1:
		return "enum"
	case p.Type == "string":
		return "text"
	case p.Type == "integer":
		return "decimal"
	}
	return ""
}

func esc(s string) string { return html.EscapeString(s) }

func admx(s *settings.PolicySchema) string {
	key := esc(s.Harness.RegistryKey)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString("<!-- " + Header + " -->\n")
	b.WriteString(`<policyDefinitions xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" revision="1.0" schemaVersion="1.0" xmlns="http://schemas.microsoft.com/GroupPolicy/2006/07/PolicyDefinitions">` + "\n")
	b.WriteString("  <policyNamespaces>\n    <target prefix=\"harness\" namespace=\"xlsft.Policies.Harness\" />\n  </policyNamespaces>\n")
	b.WriteString("  <resources minRequiredRevision=\"1.0\" />\n")
	b.WriteString("  <supportedOn>\n    <definitions>\n      <definition name=\"SUPPORTED_harness\" displayName=\"$(string.SUPPORTED_harness)\" />\n    </definitions>\n  </supportedOn>\n")
	b.WriteString("  <categories>\n    <category name=\"harness\" displayName=\"$(string.harness)\" />\n")
	for _, name := range objectNames(s) {
		fmt.Fprintf(&b, "    <category name=\"harness_%s\" displayName=\"$(string.harness_%s)\">\n      <parentCategory ref=\"harness\" />\n    </category>\n", name, name)
	}
	b.WriteString("  </categories>\n  <policies>\n")
	for _, k := range s.Keys() {
		kind := admxKind(k.SchemaProperty)
		if kind == "" {
			continue
		}
		n := policyName(k.Path)
		head := fmt.Sprintf("    <policy name=\"%s\" class=\"Machine\" displayName=\"$(string.%s)\" explainText=\"$(string.%s_Explain)\" key=\"%s\"", n, n, n, key)
		tail := fmt.Sprintf("      <parentCategory ref=\"%s\" />\n      <supportedOn ref=\"SUPPORTED_harness\" />\n", categoryOf(k.Path))
		switch kind {
		case "boolean":
			fmt.Fprintf(&b, "%s valueName=\"%s\">\n%s      <enabledValue>\n        <decimal value=\"1\" />\n      </enabledValue>\n      <disabledValue>\n        <decimal value=\"0\" />\n      </disabledValue>\n    </policy>\n", head, esc(k.Path), tail)
		case "flag":
			fmt.Fprintf(&b, "%s valueName=\"%s\">\n%s      <enabledValue>\n        <string>%s</string>\n      </enabledValue>\n    </policy>\n", head, esc(k.Path), tail, esc(k.Enum[0]))
		case "enum":
			fmt.Fprintf(&b, "%s presentation=\"$(presentation.%s)\">\n%s      <elements>\n        <enum id=\"%s\" valueName=\"%s\" required=\"true\">\n", head, n, tail, n, esc(k.Path))
			for _, e := range k.Enum {
				fmt.Fprintf(&b, "          <item displayName=\"$(string.%s_%s)\">\n            <value>\n              <string>%s</string>\n            </value>\n          </item>\n", n, e, esc(e))
			}
			b.WriteString("        </enum>\n      </elements>\n    </policy>\n")
		case "text":
			fmt.Fprintf(&b, "%s presentation=\"$(presentation.%s)\">\n%s      <elements>\n        <text id=\"%s\" valueName=\"%s\" maxLength=\"2048\" required=\"true\" />\n      </elements>\n    </policy>\n", head, n, tail, n, esc(k.Path))
		case "decimal":
			fmt.Fprintf(&b, "%s presentation=\"$(presentation.%s)\">\n%s      <elements>\n        <decimal id=\"%s\" valueName=\"%s\" required=\"true\" />\n      </elements>\n    </policy>\n", head, n, tail, n, esc(k.Path))
		}
	}
	jv := esc(s.Harness.RegistryJSONValue)
	fmt.Fprintf(&b, "    <policy name=\"%s\" class=\"Machine\" displayName=\"$(string.%s)\" explainText=\"$(string.%s_Explain)\" key=\"%s\" presentation=\"$(presentation.%s)\">\n      <parentCategory ref=\"harness\" />\n      <supportedOn ref=\"SUPPORTED_harness\" />\n      <elements>\n        <text id=\"%s\" valueName=\"%s\" maxLength=\"32767\" required=\"true\" />\n      </elements>\n    </policy>\n", jv, jv, jv, key, jv, jv, jv)
	b.WriteString("  </policies>\n</policyDefinitions>\n")
	return b.String()
}

func objectNames(s *settings.PolicySchema) []string {
	var out []string
	for name, p := range s.Properties {
		if !p.Leaf() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func jsonOnlyKeys(s *settings.PolicySchema) []string {
	var out []string
	for _, k := range s.Keys() {
		if admxKind(k.SchemaProperty) == "" {
			out = append(out, k.Path)
		}
	}
	return out
}

func adml(s *settings.PolicySchema) string {
	var strs, pres strings.Builder
	str := func(id, text string) { fmt.Fprintf(&strs, "      <string id=\"%s\">%s</string>\n", id, esc(text)) }
	str("harness", "xlsft`s harness")
	str("SUPPORTED_harness", "xlsft`s harness on Windows")
	for _, name := range objectNames(s) {
		str("harness_"+name, s.Properties[name].Title)
	}
	for _, k := range s.Keys() {
		kind := admxKind(k.SchemaProperty)
		if kind == "" {
			continue
		}
		n := policyName(k.Path)
		str(n, k.Title)
		scope := "Policy: has the force of the server's managed policy."
		if k.Harness.Scope == "machine" {
			scope = "Machine setting: read only from this machine, never from the server."
		}
		str(n+"_Explain", fmt.Sprintf("%s\n\nKey: %s (registry value %s).\n%s", k.Description, k.Path, k.Path, scope))
		switch kind {
		case "enum":
			for _, e := range k.Enum {
				str(n+"_"+e, e)
			}
			fmt.Fprintf(&pres, "      <presentation id=\"%s\">\n        <dropdownList refId=\"%s\" noSort=\"true\">%s</dropdownList>\n      </presentation>\n", n, n, esc(k.Title))
		case "text":
			fmt.Fprintf(&pres, "      <presentation id=\"%s\">\n        <textBox refId=\"%s\">\n          <label>%s</label>\n        </textBox>\n      </presentation>\n", n, n, esc(k.Title))
		case "decimal":
			fmt.Fprintf(&pres, "      <presentation id=\"%s\">\n        <decimalTextBox refId=\"%s\">%s</decimalTextBox>\n      </presentation>\n", n, n, esc(k.Title))
		}
	}
	jv := s.Harness.RegistryJSONValue
	str(jv, "Settings (JSON)")
	str(jv+"_Explain", "A JSON object with any keys of the policy, for those that are lists or dictionaries: "+strings.Join(jsonOnlyKeys(s), ", ")+". A single value of the same key, set by another policy here, wins over it.")
	fmt.Fprintf(&pres, "      <presentation id=\"%s\">\n        <textBox refId=\"%s\">\n          <label>JSON</label>\n        </textBox>\n      </presentation>\n", jv, jv)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString("<!-- " + Header + " -->\n")
	b.WriteString(`<policyDefinitionResources xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" revision="1.0" schemaVersion="1.0" xmlns="http://schemas.microsoft.com/GroupPolicy/2006/07/PolicyDefinitions">` + "\n")
	b.WriteString("  <displayName>xlsft`s harness</displayName>\n")
	b.WriteString("  <description>Server, updates and managed policy of xlsft`s harness.</description>\n")
	b.WriteString("  <resources>\n    <stringTable>\n")
	b.WriteString(strs.String())
	b.WriteString("    </stringTable>\n    <presentationTable>\n")
	b.WriteString(pres.String())
	b.WriteString("    </presentationTable>\n  </resources>\n</policyDefinitionResources>\n")
	return b.String()
}

// regFile is an example .reg: the template values as single values, and one example list in the Settings JSON.
func regFile(s *settings.PolicySchema) (string, error) {
	var b strings.Builder
	b.WriteString("Windows Registry Editor Version 5.00\n\n")
	b.WriteString("; " + Header + "\n; An example: set serverUrl, then import it with `reg import harness.reg` as an administrator.\n\n")
	fmt.Fprintf(&b, "[HKEY_LOCAL_MACHINE\\%s]\n", s.Harness.RegistryKey)
	for _, k := range s.Keys() {
		if len(k.Harness.Template) == 0 {
			continue
		}
		v, err := decode(k.Harness.Template)
		if err != nil {
			return "", err
		}
		switch x := v.(type) {
		case bool:
			n := 0
			if x {
				n = 1
			}
			fmt.Fprintf(&b, "%q=dword:%08x\n", k.Path, n)
		case string:
			fmt.Fprintf(&b, "\"%s\"=\"%s\"\n", k.Path, regEscape(x))
		default:
			return "", fmt.Errorf("%s: a template value of a single registry value must be a boolean or a string", k.Path)
		}
	}
	var example map[string]any
	if p := s.Lookup("permissions.deny"); p != nil && len(p.Examples) > 0 {
		v, err := decode(p.Examples[0])
		if err != nil {
			return "", err
		}
		example = map[string]any{"permissions": map[string]any{"deny": v}}
	}
	if example != nil {
		js, err := json.Marshal(example)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\"%s\"=\"%s\"\n", s.Harness.RegistryJSONValue, regEscape(string(js)))
	}
	return strings.ReplaceAll(b.String(), "\n", "\r\n"), nil
}

func regEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

func keysMarkdown(s *settings.PolicySchema) string {
	var b strings.Builder
	b.WriteString("<!-- " + Header + " -->\n\n")
	b.WriteString("# Machine policy keys\n\n")
	b.WriteString("Every key an administrator can set on a machine, from `packages/client/settings/policy.schema.json`. ")
	b.WriteString("On macOS they are keys of the preference domain `" + s.Harness.Domain + "`, nested as shown (`sandbox.enabled` is `enabled` in a `sandbox` dictionary). ")
	b.WriteString("On Windows a single value is a registry value named by the whole path under `HKLM\\" + s.Harness.RegistryKey + "`; lists and dictionaries go in the `" + s.Harness.RegistryJSONValue + "` value as JSON. ")
	b.WriteString("In `settings.json` and `settings.d/*.json` of the system folder they are ordinary JSON.\n\n")
	b.WriteString("**machine** keys configure the installation and are read only from these places. **policy** keys are the same as in the server's managed policy and have its force.\n\n")
	b.WriteString("| Key | Scope | Type | Windows | Template | Description |\n|---|---|---|---|---|---|\n")
	for _, k := range s.Keys() {
		typ := k.Type
		if k.Items != nil {
			typ += " of " + k.Items.Type
		}
		if len(k.Enum) > 0 {
			typ += ": " + strings.Join(k.Enum, ", ")
		}
		win := "JSON in `" + s.Harness.RegistryJSONValue + "`"
		switch admxKind(k.SchemaProperty) {
		case "boolean", "decimal":
			win = "REG_DWORD"
		case "flag", "enum", "text":
			win = "REG_SZ"
		}
		tv := ""
		if len(k.Harness.Template) > 0 {
			tv = "`" + string(k.Harness.Template) + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", k.Path, k.Harness.Scope, typ, win, tv, strings.ReplaceAll(k.Description, "|", "\\|"))
	}
	return b.String()
}
