// Package sshhost runs a session's client on another machine over SSH: saved hosts, verified host keys, finding or uploading
// harness-client there, and a tunnel to it, so a frontend reaches it like a local client.
//
// Everything goes through the user's own OpenSSH client, which brings their ~/.ssh/config, ssh-agent, ProxyJump and hardware
// keys along. This package never reads a private key or asks for a password (BatchMode): what ssh cannot do by itself fails
// with ssh's own message. Saved hosts and remembered keys are device-local, under HARNESS_HOME/ssh, and are never synced.
package sshhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Host is a saved host: an address, never a key.
type Host struct {
	ID string `json:"id"`
	// Name is what the user calls it.
	Name string `json:"name,omitempty"`
	// Destination goes to ssh: an alias of ~/.ssh/config, a host name or user@host.
	Destination string `json:"destination"`
	// Port is 0 for what ssh would use.
	Port int `json:"port,omitempty"`
	// IdentityFile is a path for ssh's -i, when the user names one. The file is never read here.
	IdentityFile string `json:"identityFile,omitempty"`
}

// A destination goes to ssh as one argument after "--", but its options parser is not the only thing reading it: a config's
// %h, a ProxyCommand. Letters, digits and what user@host and IPv6 literals need is all it may hold, and it cannot start
// like an option.
var destinationRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@:%\[\]+-]*$`)

// Validate says what is wrong with h.
func (h Host) Validate() error {
	if !destinationRE.MatchString(h.Destination) {
		return fmt.Errorf("%q is not a host: use an alias, a host name or user@host", h.Destination)
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("port %d is out of range", h.Port)
	}
	if strings.ContainsAny(h.IdentityFile, "\n\r\"") {
		return errors.New("the identity file's path has characters ssh cannot take")
	}
	return nil
}

// Store keeps the saved hosts in one file, HARNESS_HOME/ssh/hosts.json.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore is a store at path.
func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) loadLocked() ([]Host, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hosts []Host
	if err := json.Unmarshal(data, &hosts); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return hosts, nil
}

func (s *Store) saveLocked(hosts []Host) error {
	data, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns the saved hosts in the order they were added.
func (s *Store) List() ([]Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Get returns one host.
func (s *Store) Get(id string) (Host, error) {
	hosts, err := s.List()
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.ID == id {
			return h, nil
		}
	}
	return Host{}, ErrNoHost
}

// ErrNoHost is the answer about an id no saved host has.
var ErrNoHost = errors.New("no such host")

// Save adds h, or replaces the host with its id.
func (s *Store) Save(h Host) (Host, error) {
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.loadLocked()
	if err != nil {
		return Host{}, err
	}
	if h.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return Host{}, err
		}
		h.ID = "ssh_" + hex.EncodeToString(b)
		hosts = append(hosts, h)
	} else if i := slices.IndexFunc(hosts, func(x Host) bool { return x.ID == h.ID }); i >= 0 {
		hosts[i] = h
	} else {
		return Host{}, ErrNoHost
	}
	return h, s.saveLocked(hosts)
}

// Delete removes a host. Removing what is not there is fine.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts, err := s.loadLocked()
	if err != nil {
		return err
	}
	return s.saveLocked(slices.DeleteFunc(hosts, func(x Host) bool { return x.ID == id }))
}
