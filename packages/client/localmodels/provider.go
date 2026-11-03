// Package localmodels lets the client use LLM servers running on the user's machine (Ollama, LM Studio,
// llama.cpp, MLX, vLLM, Jan, or any OpenAI-compatible endpoint). The harness server cannot reach the user's
// localhost, so the client calls them directly: Router implements gateway.Gateway and sends requests for
// "local:<provider>/<model>" ids to the Registry, everything else to the server's gateway.
//
// Everything here is device-local: the provider list and settings live in HARNESS_HOME/local-providers.json,
// API keys in the OS keychain (file fallback), and nothing of it is synced. Only token usage is reported to the
// server (cost 0, flagged local) for the Usage tab, and the server's policy may disallow local models.
package localmodels

import (
	"net/url"
	"strings"
)

// IDPrefix starts every local model id: "local:<provider id>/<model name>".
const IDPrefix = "local:"

// Kind is the kind of a local provider.
type Kind string

const (
	KindOllama   Kind = "ollama"
	KindLMStudio Kind = "lmstudio"
	KindLlamaCpp Kind = "llamacpp"
	KindMLX      Kind = "mlx"
	KindVLLM     Kind = "vllm"
	KindJan      Kind = "jan"
	// KindCustom is any server speaking OpenAI chat completions.
	KindCustom Kind = "custom"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindOllama, KindLMStudio, KindLlamaCpp, KindMLX, KindVLLM, KindJan, KindCustom:
		return true
	}
	return false
}

// DisplayName is the human name of the kind.
func (k Kind) DisplayName() string {
	switch k {
	case KindOllama:
		return "Ollama"
	case KindLMStudio:
		return "LM Studio"
	case KindLlamaCpp:
		return "llama.cpp"
	case KindMLX:
		return "MLX"
	case KindVLLM:
		return "vLLM"
	case KindJan:
		return "Jan"
	}
	return "OpenAI-compatible"
}

// DefaultContextLength is the context window requested from Ollama (num_ctx) when the provider sets none. It is
// capped by each model's own limit. Ollama's own default (2048–4096) is too small for an agent's prompt.
const DefaultContextLength = 32768

// ProviderConfig is the stored configuration of one provider.
type ProviderConfig struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Enabled bool   `json:"enabled"`
	// ContextLength is the num_ctx requested from Ollama (0: DefaultContextLength).
	ContextLength int `json:"context_length,omitempty"`
	// Builtin marks the default endpoints (not stored).
	Builtin bool `json:"-"`
}

// builtins are probed on their default ports. llama.cpp and MLX share 8080; llama.cpp answers /props.
var builtins = []ProviderConfig{
	{ID: "ollama", Kind: KindOllama, Name: "Ollama", BaseURL: "http://127.0.0.1:11434", Enabled: true, Builtin: true},
	{ID: "lmstudio", Kind: KindLMStudio, Name: "LM Studio", BaseURL: "http://127.0.0.1:1234/v1", Enabled: true, Builtin: true},
	{ID: "llamacpp", Kind: KindLlamaCpp, Name: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1", Enabled: true, Builtin: true},
	{ID: "mlx", Kind: KindMLX, Name: "MLX", BaseURL: "http://127.0.0.1:8080/v1", Enabled: true, Builtin: true},
	{ID: "vllm", Kind: KindVLLM, Name: "vLLM", BaseURL: "http://127.0.0.1:8000/v1", Enabled: true, Builtin: true},
	{ID: "jan", Kind: KindJan, Name: "Jan", BaseURL: "http://127.0.0.1:1337/v1", Enabled: true, Builtin: true},
}

// Builtins returns the default providers.
func Builtins() []ProviderConfig { return append([]ProviderConfig(nil), builtins...) }

// IsLocal reports whether a model id names a local model.
func IsLocal(modelID string) bool { return strings.HasPrefix(modelID, IDPrefix) }

// ModelID builds the namespaced id of a provider's model.
func ModelID(providerID, name string) string { return IDPrefix + providerID + "/" + name }

// ParseModelID splits "local:<provider>/<name>". Names may contain slashes (Hugging Face paths).
func ParseModelID(id string) (providerID, name string, ok bool) {
	if !IsLocal(id) {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, IDPrefix)
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// rootURL is the server root of a base URL ("http://host:1234/v1" -> "http://host:1234").
func rootURL(base string) string {
	b := strings.TrimRight(base, "/")
	return strings.TrimSuffix(b, "/v1")
}

// openAIBase is the OpenAI-compatible prefix of a provider (".../v1"); custom endpoints are used as given.
func openAIBase(c ProviderConfig) string {
	b := strings.TrimRight(c.BaseURL, "/")
	switch c.Kind {
	case KindCustom:
		return b
	case KindOllama:
		return rootURL(b) + "/v1"
	}
	if !strings.HasSuffix(b, "/v1") {
		b += "/v1"
	}
	return b
}

// normalizeBaseURL validates an http(s) URL and trims trailing slashes.
func normalizeBaseURL(raw string) (string, bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return s, true
}
