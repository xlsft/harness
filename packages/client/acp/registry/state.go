package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AuthState is what is known about an agent's sign-in.
type AuthState string

const (
	AuthUnknown       AuthState = ""
	AuthNotRequired   AuthState = "not_required"
	AuthRequired      AuthState = "required"
	AuthAuthenticated AuthState = "authenticated"
)

// AuthMethod is an auth method from the agent's initialize response.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Kind: "agent", "terminal" or "env_var".
	Kind    string            `json:"kind"`
	EnvVars []string          `json:"envVars,omitempty"`
	Link    string            `json:"link,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Installed is the local state of an installed agent.
type Installed struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    Kind   `json:"kind"`
	// Binary agents: the install directory and the command relative to it.
	Dir string `json:"dir,omitempty"`
	// The launch recorded at install time (from the registry entry of that version).
	Cmd         string            `json:"cmd,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Package     string            `json:"package,omitempty"`
	InstalledAt time.Time         `json:"installedAt"`
}

// AgentState is everything stored about one agent.
type AgentState struct {
	Installed   *Installed   `json:"installed,omitempty"`
	Auth        AuthState    `json:"auth,omitempty"`
	AuthMethods []AuthMethod `json:"authMethods,omitempty"`
}

type stateFile struct {
	Agents map[string]*AgentState `json:"agents"`
}

func (m *Manager) statePath() string { return filepath.Join(m.Dir, "state.json") }

func (m *Manager) readState() stateFile {
	st := stateFile{Agents: map[string]*AgentState{}}
	b, err := os.ReadFile(m.statePath())
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Agents == nil {
		st.Agents = map[string]*AgentState{}
	}
	return st
}

func (m *Manager) updateState(fn func(st *stateFile) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.readState()
	if err := fn(&st); err != nil {
		return err
	}
	return writeJSON(m.statePath(), st)
}

// State returns the stored state of an agent (zero when nothing is stored).
func (m *Manager) State(id string) AgentState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.readState().Agents[id]; s != nil {
		return *s
	}
	return AgentState{}
}

// InstalledAgents returns the installed agents, by id.
func (m *Manager) InstalledAgents() []Installed {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Installed
	for _, s := range m.readState().Agents {
		if s.Installed != nil {
			out = append(out, *s.Installed)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetAuth records what is known about an agent's sign-in; nil methods keep the stored ones.
func (m *Manager) SetAuth(id string, state AuthState, methods []AuthMethod) error {
	if !validID(id) {
		return fmt.Errorf("invalid agent id %q", id)
	}
	return m.updateState(func(st *stateFile) error {
		s := st.Agents[id]
		if s == nil {
			s = &AgentState{}
			st.Agents[id] = s
		}
		// A sign-in through the client stays "authenticated" while sessions keep starting.
		if !(state == AuthNotRequired && s.Auth == AuthAuthenticated) {
			s.Auth = state
		}
		if methods != nil {
			s.AuthMethods = methods
		}
		return nil
	})
}

// Launch is how to start an installed agent.
type Launch struct {
	Command string
	Args    []string
	// Env is added to the agent's environment.
	Env map[string]string
}

// ErrNotInstalled is returned for an agent that is not installed.
var ErrNotInstalled = errors.New("the agent is not installed")

// Launch returns the command of an installed agent. npx and uvx need their runtime on PATH.
func (m *Manager) Launch(id string) (Launch, error) {
	st := m.State(id)
	in := st.Installed
	if in == nil {
		return Launch{}, ErrNotInstalled
	}
	switch in.Kind {
	case KindBinary:
		cmd := in.Cmd
		if !filepath.IsAbs(cmd) {
			cmd = filepath.Join(in.Dir, filepath.FromSlash(strings.ReplaceAll(cmd, `\`, "/")))
		}
		if !within(in.Dir, cmd) {
			return Launch{}, fmt.Errorf("agent %s: command %s is outside its install directory", id, in.Cmd)
		}
		if _, err := os.Stat(cmd); err != nil {
			return Launch{}, fmt.Errorf("agent %s: %w (reinstall it)", id, err)
		}
		return Launch{Command: cmd, Args: append([]string(nil), in.Args...), Env: in.Env}, nil
	case KindNpx:
		p, err := m.lookPath("npx")
		if err != nil {
			return Launch{}, fmt.Errorf("agent %s needs node (npx) on PATH", id)
		}
		return Launch{Command: p, Args: append([]string{"--yes", in.Package}, in.Args...), Env: in.Env}, nil
	case KindUvx:
		p, err := m.lookPath("uvx")
		if err != nil {
			return Launch{}, fmt.Errorf("agent %s needs uv (uvx) on PATH", id)
		}
		return Launch{Command: p, Args: append([]string{in.Package}, in.Args...), Env: in.Env}, nil
	}
	return Launch{}, fmt.Errorf("agent %s: unknown distribution %q", id, in.Kind)
}

// within reports whether p is dir or inside it (lexically).
func within(dir, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
