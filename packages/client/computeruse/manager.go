package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Manager is computer use on this client: the driver, the device-local switch and a Controller per session. The switch
// lives in Dir/settings.json under HARNESS_HOME, next to the other choices of this machine, and never reaches the
// server: it is off until the user turns it on here. Grants are held in memory and end with the session.
type Manager struct {
	// Driver overrides the platform's driver (tests).
	Driver Driver
	// Dir holds the settings file; empty keeps the switch in memory.
	Dir string
	// OnState receives each change of a session's state, for the frontends.
	OnState func(sessionID string, st State)
	// OnStop runs for a session when the user stops it from the indicator on the machine (not from a frontend): the
	// server interrupts its turn.
	OnStop func(sessionID string)
	Logger *slog.Logger

	once sync.Once
	d    Driver

	mu       sync.Mutex
	loaded   bool
	enabled  bool
	sessions map[string]*Controller
	// active are the sessions controlling the machine, with what they are doing, for the indicator.
	active       map[string]string
	indicatorSet func(string)
	indicatorEnd func()

	// input serializes actions across sessions: there is one mouse and one keyboard.
	input chan struct{}
}

type settingsFile struct {
	Enabled bool `json:"enabled"`
}

func (m *Manager) driver() Driver {
	m.once.Do(func() {
		m.d = m.Driver
		if m.d == nil {
			m.d = NewDriver()
		}
		m.input = make(chan struct{}, 1)
	})
	return m.d
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// Info describes the driver of this platform.
func (m *Manager) Info() Info { return m.driver().Info() }

// Permissions asks the driver what the OS allows. It never prompts.
func (m *Manager) Permissions(ctx context.Context) (Permissions, error) {
	if !m.driver().Info().Supported {
		return Permissions{}, nil
	}
	return m.driver().Permissions(ctx)
}

func (m *Manager) settingsPath() string {
	if m.Dir == "" {
		return ""
	}
	return filepath.Join(m.Dir, "settings.json")
}

// Enabled reports the switch. A settings file that cannot be read means off.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded {
		m.loaded = true
		if p := m.settingsPath(); p != "" {
			if data, err := os.ReadFile(p); err == nil {
				var f settingsFile
				if err := json.Unmarshal(data, &f); err == nil {
					m.enabled = f.Enabled
				}
			}
		}
	}
	return m.enabled
}

// SetEnabled changes the switch. Turning it off also stops every session.
func (m *Manager) SetEnabled(on bool) error {
	_ = m.Enabled()
	if p := m.settingsPath(); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		data, err := json.MarshalIndent(settingsFile{Enabled: on}, "", "  ")
		if err != nil {
			return err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.enabled = on
	var all []*Controller
	if !on {
		for _, c := range m.sessions {
			all = append(all, c)
		}
	}
	m.mu.Unlock()
	for _, c := range all {
		c.Stop()
	}
	return nil
}

// Session returns the session's Controller, creating it.
func (m *Manager) Session(id string) *Controller {
	m.driver()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]*Controller{}
	}
	c := m.sessions[id]
	if c == nil {
		c = newController(m, id)
		m.sessions[id] = c
	}
	return c
}

// Lookup returns the session's Controller if it has one.
func (m *Manager) Lookup(id string) *Controller {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// CloseSession ends a session's computer use: its grants go with it.
func (m *Manager) CloseSession(id string) {
	m.mu.Lock()
	c := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// StopAll stops every session (the client is shutting down).
func (m *Manager) StopAll() {
	m.mu.Lock()
	var all []*Controller
	for _, c := range m.sessions {
		all = append(all, c)
	}
	m.mu.Unlock()
	for _, c := range all {
		c.Stop()
	}
}

// acquire takes the machine's input for one action.
func (m *Manager) acquire(ctx context.Context) (func(), error) {
	select {
	case m.input <- struct{}{}:
		return func() { <-m.input }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// stateChanged is called by Controllers; it drives the frontends' indicator and the one on the machine.
func (m *Manager) stateChanged(sessionID string, st State) {
	if m.OnState != nil {
		m.OnState(sessionID, st)
	}
	m.updateIndicator(sessionID, st)
}

func (m *Manager) updateIndicator(sessionID string, st State) {
	ind, ok := m.driver().(Indicator)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]string{}
	}
	if st.Controlling {
		m.active[sessionID] = st.Action
	} else {
		delete(m.active, sessionID)
	}
	if len(m.active) == 0 {
		if m.indicatorEnd != nil {
			m.indicatorEnd()
			m.indicatorSet, m.indicatorEnd = nil, nil
		}
		return
	}
	label := m.indicatorLabelLocked()
	if m.indicatorEnd != nil {
		m.indicatorSet(label)
		return
	}
	set, hide, err := ind.ShowIndicator(label, m.stopFromIndicator)
	if err != nil {
		m.log().Warn("computer use: cannot show the indicator", "error", err)
		return
	}
	m.indicatorSet, m.indicatorEnd = set, hide
}

func (m *Manager) indicatorLabelLocked() string {
	for _, a := range m.active {
		if a = strings.TrimSpace(a); a != "" {
			return "Harness: " + a
		}
	}
	return "Harness is controlling this machine"
}

// stopFromIndicator stops every session that is controlling the machine.
func (m *Manager) stopFromIndicator() {
	m.mu.Lock()
	var ids []string
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if c := m.Lookup(id); c != nil {
			c.Stop()
		}
		if m.OnStop != nil {
			m.OnStop(id)
		}
	}
}

// ErrUnsupportedPlatform is what the stub drivers report.
var ErrUnsupportedPlatform = errors.New("computer use is not supported on this platform yet")
