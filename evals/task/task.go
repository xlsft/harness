// Package task loads eval task definitions from tasks/<id>/task.yaml.
package task

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Task is one eval task loaded from tasks/<id>/task.yaml.
type Task struct {
	ID       string   `yaml:"id" json:"id"`
	Title    string   `yaml:"title" json:"title"`
	Language string   `yaml:"language" json:"language"`
	Tags     []string `yaml:"tags" json:"tags"`
	Prompt   string   `yaml:"prompt" json:"-"`
	Timeout  Duration `yaml:"timeout" json:"timeout"`
	MaxTurns int      `yaml:"max_turns" json:"max_turns"`

	Dir string `yaml:"-" json:"-"` // absolute path of tasks/<id>
}

// Duration is a time.Duration that reads "5m" style strings from YAML.
type Duration time.Duration

// UnmarshalYAML parses a Go duration string such as "5m".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText renders the duration as a Go duration string.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

const (
	DefaultTimeout  = 5 * time.Minute
	DefaultMaxTurns = 30
)

// LoadAll loads every task under dir, or only the given ids, sorted by id.
func LoadAll(dir string, only []string) ([]*Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	var tasks []*Task
	for _, e := range entries {
		if !e.IsDir() || (len(want) > 0 && !want[e.Name()]) {
			continue
		}
		t, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		delete(want, e.Name())
		tasks = append(tasks, t)
	}
	for id := range want {
		return nil, fmt.Errorf("unknown task %q", id)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Load reads and validates one task directory.
func Load(dir string) (*Task, error) {
	data, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
	if err != nil {
		return nil, err
	}
	t := &Task{}
	if err := yaml.Unmarshal(data, t); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	t.Dir = abs
	switch {
	case t.ID != filepath.Base(dir):
		return nil, fmt.Errorf("id %q does not match directory name", t.ID)
	case t.Prompt == "":
		return nil, fmt.Errorf("prompt is empty")
	}
	for _, f := range []string{"repo", "check.sh"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, err
		}
	}
	if t.Timeout <= 0 {
		t.Timeout = Duration(DefaultTimeout)
	}
	if t.MaxTurns <= 0 {
		t.MaxTurns = DefaultMaxTurns
	}
	return t, nil
}
