// Package sidechat implements side chats: separate, lightweight conversations attached to a session that see
// the main session's conversation but never change it (proto/harness/agent/v1/sidechat.proto).
//
// A side chat is one JSON file next to the session's transcript (<session dir>/side-chats/<id>.json, where the
// session dir is the transcript path without ".jsonl"); Store reads and writes them. Request builds the model
// request of a question (system prompt, a compact rendering of the main session, the side chat's history and
// the question with its quote) and Ask streams the answer. No tools are ever offered.
package sidechat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/llm"
)

// DefaultTitle names a side chat until its first question.
const DefaultTitle = "Side chat"

// ErrNotFound is returned for an unknown side chat id.
var ErrNotFound = errors.New("side chat not found")

// Message is one stored message of a side chat.
type Message struct {
	ID string `json:"id"`
	// Role is llm.RoleUser or llm.RoleAssistant.
	Role string `json:"role"`
	Text string `json:"text"`
	// Quote is the excerpt a user question is about.
	Quote string `json:"quote,omitempty"`
	// Model and Effort answered an assistant message.
	Model     string     `json:"model,omitempty"`
	Effort    string     `json:"effort,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	Usage     *llm.Usage `json:"usage,omitempty"`
	// Stopped: the answer was cut short by the user; Error: the request failed.
	Stopped bool   `json:"stopped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Chat is a side chat with its messages.
type Chat struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	// TitleSet: the user named it (the first question no longer names it).
	TitleSet  bool      `json:"titleSet,omitempty"`
	Model     string    `json:"model"`
	Effort    string    `json:"effort,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  []Message `json:"messages"`
}

// Store keeps the side chats of one session in Dir.
type Store struct {
	Dir string
}

// fileLocks serializes writes to one side chat file across Store values.
var fileLocks sync.Map

func lockFor(path string) *sync.Mutex {
	m, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// DirFor returns the side chat directory of the session whose transcript is at transcriptPath.
func DirFor(transcriptPath string) string {
	return filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "side-chats")
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewID returns a new side chat or message id.
func NewID(prefix string) string { return prefix + uuid.NewString() }

func (s Store) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

// List returns the side chats, oldest first.
func (s Store) List() ([]*Chat, error) {
	files, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Chat
	for _, f := range files {
		c, err := readChat(f)
		if err != nil {
			continue // a damaged file must not hide the others
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get reads one side chat.
func (s Store) Get(id string) (*Chat, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	c, err := readChat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return c, err
}

// Save writes a side chat atomically.
func (s Store) Save(c *Chat) error {
	p, err := s.path(c.ID)
	if err != nil {
		return fmt.Errorf("invalid side chat id %q", c.ID)
	}
	mu := lockFor(p)
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Update reads a side chat, applies fn and saves it, under the file's lock.
func (s Store) Update(id string, fn func(*Chat) error) (*Chat, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	mu := lockFor(p + "#update")
	mu.Lock()
	defer mu.Unlock()
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	c.UpdatedAt = time.Now().UTC()
	if err := s.Save(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Delete removes a side chat. It waits for an Update in progress, so an Update afterwards finds nothing.
func (s Store) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	mu := lockFor(p + "#update")
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func readChat(path string) (*Chat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// TitleFrom names a side chat after its first question: the first line, at most 60 characters.
func TitleFrom(question string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(question), "\n", 2)[0])
	if line == "" {
		return DefaultTitle
	}
	r := []rune(line)
	if len(r) > 60 {
		return strings.TrimSpace(string(r[:60])) + "…"
	}
	return line
}
