package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/settings"
)

// Spec says how to run the language server of one language.
type Spec struct {
	// ID is the name settings use (lspServers.<id>).
	ID string
	// Extensions maps a file extension, with its dot and in lower case, to the protocol's language id.
	Extensions map[string]string
	// Commands are the ways to run a server, best first; the first one installed is used.
	Commands [][]string
	// RootMarkers name what marks a language root, by priority: the nearest directory with the first marker wins over
	// any directory with a later one.
	RootMarkers []string
	// InitOptions go to the server as initializationOptions.
	InitOptions json.RawMessage

	disabled bool
}

// builtinSpecs is the table of known servers, one entry per language. Add a language here, or per user with the
// lspServers setting.
var builtinSpecs = []Spec{
	{
		ID:          "go",
		Extensions:  map[string]string{".go": "go"},
		Commands:    [][]string{{"gopls"}},
		RootMarkers: []string{"go.work", "go.mod"},
	},
	{
		ID: "typescript",
		Extensions: map[string]string{".ts": "typescript", ".tsx": "typescriptreact", ".mts": "typescript", ".cts": "typescript",
			".js": "javascript", ".jsx": "javascriptreact", ".mjs": "javascript", ".cjs": "javascript"},
		Commands:    [][]string{{"typescript-language-server", "--stdio"}, {"vtsls", "--stdio"}},
		RootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json"},
	},
	{
		ID:          "python",
		Extensions:  map[string]string{".py": "python", ".pyi": "python"},
		Commands:    [][]string{{"pyright-langserver", "--stdio"}},
		RootMarkers: []string{"pyrightconfig.json", "pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"},
	},
	{
		ID:          "rust",
		Extensions:  map[string]string{".rs": "rust"},
		Commands:    [][]string{{"rust-analyzer"}},
		RootMarkers: []string{"Cargo.lock", "Cargo.toml"},
	},
}

// specTable applies the lspServers setting to the built-in table. A language the table does not know is added when it
// has a command and extensions; added languages win over built-in ones for an extension they share.
func specTable(over map[string]settings.LSPServer) []Spec {
	ids := make([]string, 0, len(over))
	for id := range over {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Spec
	for _, id := range ids {
		o := over[id]
		if slices.ContainsFunc(builtinSpecs, func(s Spec) bool { return s.ID == id }) || o.Command == "" || len(o.Extensions) == 0 {
			continue
		}
		out = append(out, applyOverride(Spec{ID: id}, o))
	}
	for _, s := range builtinSpecs {
		if o, ok := over[s.ID]; ok {
			s = applyOverride(s, o)
		}
		out = append(out, s)
	}
	return out
}

func applyOverride(s Spec, o settings.LSPServer) Spec {
	if o.Command != "" {
		s.Commands = [][]string{append([]string{o.Command}, o.Args...)}
	}
	if len(o.Extensions) > 0 {
		language := o.LanguageID
		if language == "" {
			language = s.ID
		}
		s.Extensions = map[string]string{}
		for _, e := range o.Extensions {
			s.Extensions["."+strings.ToLower(strings.TrimPrefix(e, "."))] = language
		}
	} else if o.LanguageID != "" {
		ext := make(map[string]string, len(s.Extensions))
		for e := range s.Extensions {
			ext[e] = o.LanguageID
		}
		s.Extensions = ext
	}
	if len(o.RootMarkers) > 0 {
		s.RootMarkers = o.RootMarkers
	}
	if len(o.InitializationOptions) > 0 {
		s.InitOptions = o.InitializationOptions
	}
	s.disabled = o.Disabled
	return s
}

// specFor finds the spec of a file by its extension, and the file's language id.
func specFor(table []Spec, path string) (*Spec, string) {
	ext := strings.ToLower(filepath.Ext(path))
	for i := range table {
		if language, ok := table[i].Extensions[ext]; ok {
			return &table[i], language
		}
	}
	return nil, ""
}

// notInstalledError says that no server of a language is on PATH, and which would be needed.
type notInstalledError struct {
	names []string
	ext   string
}

func (e *notInstalledError) Error() string {
	return fmt.Sprintf("No language server for %s files is installed: this needs %s on PATH. The client does not install servers; tell the user if it matters, and use Grep and Read meanwhile.", e.ext, strings.Join(e.names, " or "))
}

// resolve finds the first installed command of a spec in the PATH of environ.
func (s *Spec) resolve(environ []string, ext string) (name, bin string, args []string, err error) {
	path := envValue(environ, "PATH")
	var bad error
	names := make([]string, 0, len(s.Commands))
	for _, c := range s.Commands {
		names = append(names, c[0])
		switch p, err := lookCommand(c[0], path); {
		case err == nil:
			return c[0], p, c[1:], nil
		case !errors.Is(err, os.ErrNotExist):
			bad = err
		}
	}
	if bad != nil {
		return "", "", nil, bad
	}
	return "", "", nil, &notInstalledError{names: names, ext: ext}
}

// lookCommand finds a command: an absolute path (or one under ~/), or a bare name on PATH. A relative path, or a
// relative PATH entry such as ".", would resolve inside the repository the session works on, so it is never used: what
// runs on opening a file must not come from there.
func lookCommand(name, path string) (string, error) {
	if strings.HasPrefix(name, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			name = filepath.Join(h, name[2:])
		}
	}
	if filepath.IsAbs(name) {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("%s: %w", name, os.ErrNotExist)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("language server command %q must be a bare name on PATH or an absolute path", name)
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, candidate := range executableNames(name) {
			if p := filepath.Join(dir, candidate); isExecutable(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s: %w", name, os.ErrNotExist)
}

func executableNames(name string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(name) != "" {
		return []string{name}
	}
	return []string{name + ".exe", name + ".cmd", name + ".bat"}
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode().Perm()&0o111 != 0
}

func envValue(environ []string, key string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// realPath resolves symlinks where it can: servers report real paths, and what a path really is decides where a file
// belongs.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func isInside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// containingDir returns the deepest of dirs that holds path ("" for none).
func containingDir(path string, dirs []string) string {
	best := ""
	for _, d := range dirs {
		if isInside(path, d) && len(d) > len(best) {
			best = d
		}
	}
	return best
}

// findRoot picks the language root of a file; every path is real. Inside a working directory the search for markers
// goes up to the top of its git repository (to the working directory itself outside git) and falls back to the working
// directory, which is returned too. Outside the working directories the search goes up to the filesystem root and
// falls back to the file's directory, and workDir is "".
func findRoot(file string, markers []string, workDirs []string) (root, workDir string) {
	start := filepath.Dir(file)
	workDir = containingDir(file, workDirs)
	limit := workDir
	if workDir != "" {
		for d := workDir; ; d = filepath.Dir(d) {
			if exists(filepath.Join(d, ".git")) {
				limit = d
				break
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	for _, marker := range markers {
		for d := start; ; d = filepath.Dir(d) {
			if exists(filepath.Join(d, marker)) {
				return d, workDir
			}
			if d == limit || d == filepath.Dir(d) {
				break
			}
		}
	}
	if workDir != "" {
		return workDir, workDir
	}
	return start, ""
}
