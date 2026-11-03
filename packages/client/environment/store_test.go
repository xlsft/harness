package environment

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestStoreRoundTripFileKey(t *testing.T) {
	t.Setenv("HARNESS_KEYRING", "0")
	dir := filepath.Join(t.TempDir(), "environments")
	folder := t.TempDir()
	s := NewStore(dir)
	if env, err := s.Get(folder); env != nil || err != nil {
		t.Fatalf("empty store: %v %v", env, err)
	}
	in := Environment{Folder: folder, Name: "api", Worktree: true, BaseRef: "main",
		Additional: []Folder{{Path: "/srv/shared", Worktree: true, BaseRef: "dev"}},
		Env:        "# keys\nAPI_TOKEN=\"s3cr3t-value\"\n"}
	put, err := s.Put(in)
	if err != nil {
		t.Fatal(err)
	}
	if put.UpdatedAt.IsZero() || put.Folder != Key(folder) {
		t.Fatalf("put %+v", put)
	}
	got, err := NewStore(dir).Get(folder) // a fresh store reads the key again
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "api" || !got.Worktree || got.BaseRef != "main" || got.Env != in.Env || len(got.Additional) != 1 || got.Additional[0] != in.Additional[0] {
		t.Fatalf("got %+v", got)
	}

	// Files are private and the value is not in plain text.
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	for _, p := range append(files, filepath.Join(dir, "key")) {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, err, st.Mode())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	data, _ := os.ReadFile(files[0])
	if strings.Contains(string(data), "s3cr3t") || strings.Contains(string(data), "API_TOKEN") {
		t.Fatalf("plain text on disk:\n%s", data)
	}

	// A sealed text is bound to its folder.
	other := t.TempDir()
	var f file
	_ = json.Unmarshal(data, &f)
	f.Folder = Key(other)
	moved, _ := json.Marshal(f)
	if err := os.WriteFile(s.path(Key(other)), moved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Get(other); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Fatalf("moved sealed text: %v", err)
	}

	// Invalid env text is refused; Delete removes the file.
	var pe *ParseError
	if _, err := s.Put(Environment{Folder: folder, Env: "1X=2"}); !errors.As(err, &pe) {
		t.Fatalf("invalid put: %v", err)
	}
	if err := s.Delete(folder); err != nil {
		t.Fatal(err)
	}
	if env, err := s.Get(folder); env != nil || err != nil {
		t.Fatalf("after delete: %v %v", env, err)
	}
}

func TestStoreWithoutVariablesNeedsNoKey(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, Keys: failingKeys{}}
	if _, err := s.Put(Environment{Folder: t.TempDir(), Name: "x", Env: "  \n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); !os.IsNotExist(err) {
		t.Fatalf("key file created: %v", err)
	}
}

type failingKeys struct{}

func (failingKeys) Key(bool) ([]byte, error) { return nil, errors.New("no keychain") }

func TestKeyringKey(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	src := KeyringKey{Fallback: FileKey{Path: filepath.Join(dir, "key")}}
	if _, err := src.Key(false); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key yet: %v", err)
	}
	k, err := src.Key(true)
	if err != nil || len(k) != 32 {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); !os.IsNotExist(err) {
		t.Fatal("the key went to the file although the keychain works")
	}
	stored, err := keyring.Get(keyringService, keyringUser)
	if err != nil || stored != base64.StdEncoding.EncodeToString(k) {
		t.Fatalf("keychain: %v", err)
	}
	again, _ := src.Key(false)
	if string(again) != string(k) {
		t.Fatal("key changed")
	}

	// Without a keychain the key goes to the 0600 file, and is found there later.
	keyring.MockInitWithError(errors.New("no keychain"))
	dir2 := t.TempDir()
	src2 := KeyringKey{Fallback: FileKey{Path: filepath.Join(dir2, "key")}}
	k2, err := src2.Key(true)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir2, "key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("fallback file: %v", err)
	}
	keyring.MockInit()
	if got, err := src2.Key(false); err != nil || string(got) != string(k2) {
		t.Fatalf("fallback key not used: %v", err)
	}
}
