package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Skills installed from external search live with the personal ones, under HARNESS_HOME/skills, because that is where people look. A
// stranger's text must not start giving the model instructions by arriving, so such a folder stays out of the catalog until this device
// approved exactly its files, and again whenever they stop matching. The record is kept outside the skills folder: nothing a download
// holds can speak for itself.

const (
	externalStateFile = "skills-external.json"
	// Limits on one download. A skill is instructions and a few scripts.
	maxExternalFiles = 256
	maxExternalBytes = 8 << 20
)

// ExternalAudit is what a source said about a skill when it was stored.
type ExternalAudit struct {
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	Summary   string `json:"summary,omitempty"`
	AuditedAt string `json:"auditedAt,omitempty"`
	RiskLevel string `json:"riskLevel,omitempty"`
}

// ExternalRecord is a skill stored from external search.
type ExternalRecord struct {
	// ID and Source are the source's; Name is the folder's.
	ID     string `json:"id"`
	Source string `json:"source"`
	Name   string `json:"name"`
	// PinnedHash is the source's hash at install. ContentHash is the files as written, which is what is compared and approved.
	PinnedHash   string          `json:"pinnedHash"`
	ContentHash  string          `json:"contentHash"`
	ApprovedHash string          `json:"approvedHash,omitempty"`
	ApprovedAt   string          `json:"approvedAt,omitempty"`
	Audits       []ExternalAudit `json:"audits,omitempty"`
	License      string          `json:"license,omitempty"`
	Installs     int64           `json:"installs,omitempty"`
	InstalledAt  string          `json:"installedAt"`
}

// ExternalFile is a file of a download.
type ExternalFile struct{ Path, Contents string }

var (
	externalMu     sync.Mutex
	externalNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
)

// ExternalSkillName is the folder a skill of this slug goes in: letters, digits, "-" and "_".
func ExternalSkillName(slug string) string {
	return strings.Trim(externalNameRe.ReplaceAllString(slug, "-"), "-_")
}

func readExternal(home string) (map[string]*ExternalRecord, error) {
	out := map[string]*ExternalRecord{}
	data, err := os.ReadFile(filepath.Join(home, externalStateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", externalStateFile, err)
	}
	return out, nil
}

func writeExternal(home string, st map[string]*ExternalRecord) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	p := filepath.Join(home, externalStateFile)
	if err := os.WriteFile(p+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// ListExternal returns what is stored, by name.
func ListExternal(home string) (map[string]*ExternalRecord, error) {
	externalMu.Lock()
	defer externalMu.Unlock()
	return readExternal(home)
}

// HashSkillDir hashes a folder's files: their relative paths and bytes. A link isn't followed and counts by its target.
func HashSkillDir(dir string) (string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.Sort(rels)
	h := sha256.New()
	for _, rel := range rels {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%o\x00", rel, st.Mode().Perm()&0o111)
		if st.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return "", err
			}
			h.Write([]byte(target))
		} else {
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			h.Write(data)
		}
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// HeldExternal returns what says a folder waits: it was installed from search and isn't approved as it is now. A state file that can't
// be read can't tell those from the rest, so it holds all of the personal folder.
func HeldExternal(home string) func(dir string) bool {
	st, err := ListExternal(home)
	if err != nil {
		return func(dir string) bool { return filepath.Dir(dir) == filepath.Join(home, "skills") }
	}
	if len(st) == 0 {
		return nil
	}
	root := filepath.Join(home, "skills")
	return func(dir string) bool {
		if filepath.Dir(dir) != root {
			return false
		}
		rec := st[filepath.Base(dir)]
		if rec == nil {
			return false
		}
		if rec.ApprovedHash == "" {
			return true
		}
		hash, err := HashSkillDir(dir)
		return err != nil || hash != rec.ApprovedHash
	}
}

// cleanExternalPath checks a path a source sent: relative, inside the folder, no surprises.
func cleanExternalPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || strings.Contains(p, "\\") || path.IsAbs(p) {
		return "", fmt.Errorf("the file name %q can't be used", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("the file name %q leaves the skill's folder", p)
	}
	return clean, nil
}

// InstallExternal writes a download into HARNESS_HOME/skills and records it, unapproved. It won't replace a skill that didn't come from
// search; replacing one that did asks for approval again. Files are written without the executable bit.
func InstallExternal(home string, rec ExternalRecord, files []ExternalFile) (*ExternalRecord, error) {
	name := ExternalSkillName(rec.Name)
	if name == "" {
		return nil, errors.New("this skill has no name that can be a folder")
	}
	rec.Name = name
	if len(files) == 0 || len(files) > maxExternalFiles {
		return nil, fmt.Errorf("this skill has %d files; one to %d can be installed", len(files), maxExternalFiles)
	}
	clean := make([]ExternalFile, 0, len(files))
	total, hasSkill := 0, false
	for _, f := range files {
		p, err := cleanExternalPath(f.Path)
		if err != nil {
			return nil, err
		}
		total += len(f.Contents)
		hasSkill = hasSkill || p == SkillFile
		clean = append(clean, ExternalFile{Path: p, Contents: f.Contents})
	}
	if total > maxExternalBytes {
		return nil, errors.New("this skill is too large to install")
	}
	if !hasSkill {
		return nil, fmt.Errorf("this skill has no %s", SkillFile)
	}
	externalMu.Lock()
	defer externalMu.Unlock()
	st, err := readExternal(home)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "skills")
	dir := filepath.Join(root, name)
	if _, err := os.Lstat(dir); err == nil && st[name] == nil {
		return nil, fmt.Errorf("a personal skill named %s is already there", name)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for _, f := range clean {
		p := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(f.Contents), 0o644); err != nil {
			return nil, err
		}
	}
	hash, err := HashSkillDir(tmp)
	if err != nil {
		return nil, err
	}
	rec.ContentHash, rec.ApprovedHash, rec.ApprovedAt, rec.InstalledAt = hash, "", "", time.Now().UTC().Format(time.RFC3339)
	// The record first: a folder without one would load as a plain personal skill.
	st[name] = &rec
	if err := writeExternal(home, st); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ApproveExternal lets a stored skill run as it is now. hash is what was shown: other content is refused.
func ApproveExternal(home, name, hash string) error {
	externalMu.Lock()
	defer externalMu.Unlock()
	st, err := readExternal(home)
	if err != nil {
		return err
	}
	rec := st[name]
	if rec == nil {
		return fmt.Errorf("%s wasn't installed from search", name)
	}
	now, err := HashSkillDir(filepath.Join(home, "skills", name))
	if err != nil {
		return err
	}
	if hash == "" || hash != now {
		return fmt.Errorf("%s changed since you looked at it; look again", name)
	}
	if now != rec.ContentHash {
		return fmt.Errorf("the files of %s no longer match what was installed; install it again", name)
	}
	rec.ApprovedHash, rec.ApprovedAt = hash, time.Now().UTC().Format(time.RFC3339)
	return writeExternal(home, st)
}

// RevokeExternal takes an approval back. The skill stops loading.
func RevokeExternal(home, name string) error {
	externalMu.Lock()
	defer externalMu.Unlock()
	st, err := readExternal(home)
	if err != nil {
		return err
	}
	rec := st[name]
	if rec == nil {
		return fmt.Errorf("%s wasn't installed from search", name)
	}
	rec.ApprovedHash, rec.ApprovedAt = "", ""
	return writeExternal(home, st)
}

// RemoveExternal deletes a stored skill and its record.
func RemoveExternal(home, name string) error {
	externalMu.Lock()
	defer externalMu.Unlock()
	st, err := readExternal(home)
	if err != nil {
		return err
	}
	if st[name] == nil {
		return fmt.Errorf("%s wasn't installed from search", name)
	}
	// The folder first: a folder without a record would load as a plain personal skill.
	if err := os.RemoveAll(filepath.Join(home, "skills", name)); err != nil {
		return err
	}
	delete(st, name)
	return writeExternal(home, st)
}
