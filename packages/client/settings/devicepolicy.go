package settings

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// A policy can also reach a machine without the server: as a file an installer or configuration tool puts in a system
// folder, or through device management. It has the same keys and the same force as a POLICY document, and sits below the
// documents the server delivers. Nobody without administrator rights can write any of these places, which is why a hook in
// one needs no decision of the device's owner, unlike a hook delivered by the server.

// ManagedRoot is the system folder of the policy file: <root>/settings.json, then <root>/settings.d/*.json in name order.
var ManagedRoot = defaultManagedRoot()

// MacPreferences is the property list a macOS profile for the domain com.xlsft.harness is written to. Windows reads
// WindowsPolicyKey's Settings value, a JSON string, and single values named by a key's dotted path (parseRegQuery). Empty
// turns them off.
var (
	MacPreferences   = defaultMacPreferences()
	WindowsPolicyKey = defaultWindowsPolicyKey()
)

func defaultManagedRoot() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/harness"
	case "windows":
		return `C:\Program Files\harness`
	}
	return "/etc/harness"
}

// The places device management writes to; policy.schema.json names the same (x-harness), for the templates.
const (
	macPreferencesPath = "/Library/Managed Preferences/com.xlsft.harness.plist"
	windowsPolicyKey   = `HKLM\SOFTWARE\Policies\xlsft\harness`
)

func defaultMacPreferences() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	return macPreferencesPath
}

func defaultWindowsPolicyKey() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return windowsPolicyKey
}

// DevicePolicy is one policy that came with the machine.
type DevicePolicy struct {
	// Origin is its path, or the registry key.
	Origin  string
	Content []byte
}

// devicePolicyTTL keeps a session from starting a process for every settings read: the two external readers cost one each.
const devicePolicyTTL = 30 * time.Second

var deviceCache struct {
	sync.Mutex
	key  string
	at   time.Time
	list []DevicePolicy
}

// ResetDevicePolicyCache forgets what was read (tests, and whoever just changed the files).
func ResetDevicePolicyCache() {
	deviceCache.Lock()
	defer deviceCache.Unlock()
	deviceCache.at = time.Time{}
}

// DevicePolicies reads the machine's policies: device management first, then the file, then its drop-ins. What can't be
// read is left out: a device without any is the normal case, and a broken one is reported when it is parsed.
func DevicePolicies() []DevicePolicy {
	deviceCache.Lock()
	defer deviceCache.Unlock()
	key := ManagedRoot + "\x00" + MacPreferences + "\x00" + WindowsPolicyKey
	if deviceCache.key == key && time.Since(deviceCache.at) < devicePolicyTTL {
		return deviceCache.list
	}
	var out []DevicePolicy
	add := func(origin string, content []byte) {
		if len(strings.TrimSpace(string(content))) > 0 {
			out = append(out, DevicePolicy{Origin: origin, Content: content})
		}
	}
	if MacPreferences != "" {
		if data, ok := readPlistJSON(MacPreferences); ok {
			add(MacPreferences, data)
		}
	}
	if WindowsPolicyKey != "" {
		if data, ok := readRegistryJSON(WindowsPolicyKey); ok {
			add(WindowsPolicyKey, data)
		}
	}
	if ManagedRoot != "" {
		main := filepath.Join(ManagedRoot, "settings.json")
		if data, err := os.ReadFile(main); err == nil {
			add(main, data)
		}
		matches, _ := filepath.Glob(filepath.Join(ManagedRoot, "settings.d", "*.json"))
		sort.Strings(matches)
		for _, p := range matches {
			if data, err := os.ReadFile(p); err == nil {
				add(p, data)
			}
		}
	}
	deviceCache.key, deviceCache.at, deviceCache.list = key, time.Now(), out
	return out
}

// DevicePolicyDocs gives the machine's policies the form of org POLICY documents, for what reads those: mode policy,
// sandbox, plugin sources, and the "set by your administrator" view.
func DevicePolicyDocs() []*syncv1.Document {
	var out []*syncv1.Document
	for _, p := range DevicePolicies() {
		out = append(out, &syncv1.Document{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "device:" + p.Origin, Content: string(p.Content)})
	}
	return out
}

func readPlistJSON(path string) ([]byte, bool) {
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", "--", path).Output()
	if err != nil || !json.Valid(out) {
		return nil, false
	}
	return flattenPlist(out), true
}

// flattenPlist unwraps the Settings key a profile may keep the JSON in, as a string or a dictionary.
func flattenPlist(data []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return data
	}
	inner, ok := top["Settings"]
	if !ok {
		return data
	}
	var s string
	if json.Unmarshal(inner, &s) == nil {
		return []byte(s)
	}
	return inner
}

func readRegistryJSON(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "reg", "query", key).Output()
	if err != nil {
		return nil, false
	}
	return parseRegQuery(string(out))
}

// parseRegQuery reads the values of the key out of `reg query` output: the Settings value, a JSON string, and the single
// values the administrative template writes, one per key of the policy schema, named by its dotted path
// ("sandbox.enabled"). A single value wins over the same key in Settings. Values the schema doesn't know are left out.
func parseRegQuery(out string) ([]byte, bool) {
	schema := Schema()
	doc := map[string]any{}
	found := false
	var singles [][3]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "HKEY_") {
			continue
		}
		parts := strings.SplitN(line, "    ", 3)
		if len(parts) < 2 || !strings.HasPrefix(parts[1], "REG_") {
			continue
		}
		name, typ, value := parts[0], parts[1], ""
		if len(parts) == 3 {
			value = strings.TrimSpace(parts[2])
		}
		if name == schema.Harness.RegistryJSONValue && typ == "REG_SZ" {
			if err := json.Unmarshal([]byte(value), &doc); err != nil {
				// Broken: passed on as it is, so the settings loader reports it.
				return []byte(value), true
			}
			found = true
			continue
		}
		singles = append(singles, [3]string{name, typ, value})
	}
	for _, v := range singles {
		p := schema.Lookup(v[0])
		if p == nil {
			continue
		}
		val, ok := registryValue(p, v[1], v[2])
		if !ok {
			continue
		}
		setPath(doc, strings.Split(v[0], "."), val)
		found = true
	}
	if !found {
		return nil, false
	}
	b, err := json.Marshal(doc)
	return b, err == nil
}

// registryValue converts one value as `reg query` prints it to the type the schema gives its key.
func registryValue(p *SchemaProperty, typ, value string) (any, bool) {
	switch p.Type {
	case "boolean", "integer":
		if typ != "REG_DWORD" {
			return nil, false
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 32)
		if err != nil {
			return nil, false
		}
		if p.Type == "boolean" {
			return n != 0, true
		}
		return n, true
	case "string":
		return value, typ == "REG_SZ" || typ == "REG_EXPAND_SZ"
	}
	return nil, false
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
