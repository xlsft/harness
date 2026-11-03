package speech

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Local provider ids. Every local engine runs on this machine inside the client's process tree: Parakeet, Whisper
// and Piper through the sherpa-onnx runtime that the client downloads into HARNESS_HOME (nothing to install by
// hand), macOS say through the operating system.
const (
	ProviderParakeet = "parakeet"
	ProviderWhisper  = "whisper"
	ProviderPiper    = "piper"
	ProviderSay      = "macos-say"
	// ProviderBrowser is the frontend's Web Speech API: never run by the client.
	ProviderBrowser = "browser"
	// ProviderRuntime holds the sherpa-onnx runtime (RuntimeKey).
	ProviderRuntime = "runtime"
)

// RuntimeKey is the catalog key of the sherpa-onnx runtime that the Parakeet, Whisper and Piper models need.
const RuntimeKey = ProviderRuntime + "/sherpa-onnx"

// SherpaVersion is the sherpa-onnx release the client downloads.
const SherpaVersion = "v1.13.8"

// Kind is STT or TTS (0: neither, the runtime).
type Kind int

const (
	KindSTT Kind = iota + 1
	KindTTS
)

// RemoteFile is one file of a model download.
type RemoteFile struct {
	URL string
	// Name is the file name in the model's directory (plain files).
	Name string
	// Archive: a .tar.bz2 whose root folder is stripped; Keep picks the entries to extract by their path below
	// it (nil: every regular file at its top level).
	Archive bool
	Keep    func(rel string) bool
	// Size is the approximate download size, for progress before the server says.
	Size int64
}

// LocalModel is a downloadable model of a local engine, or the runtime.
type LocalModel struct {
	Provider    string
	ID          string
	DisplayName string
	Description string
	Kind        Kind
	Languages   []string
	Files       []RemoteFile
	// Required are the paths (relative to the model's directory) that must exist for it to be installed.
	Required []string
	// Needs are catalog keys the same download installs first (the runtime).
	Needs []string
	// Recommended marks the default model of its engine.
	Recommended bool
}

// Key is "<provider>/<id>".
func (m LocalModel) Key() string { return m.Provider + "/" + m.ID }

// Size is the approximate download size.
func (m LocalModel) Size() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

const (
	releases = "https://" + "github.com/k2-fsa/sherpa-onnx/releases/download/"
	asrBase  = releases + "asr-models/"
	ttsBase  = releases + "tts-models/"
)

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Binaries of the runtime, relative to its directory.
var (
	SherpaOffline    = "bin/" + exe("sherpa-onnx-offline")
	SherpaOfflineTTS = "bin/" + exe("sherpa-onnx-offline-tts")
)

// runtimeAsset is the sherpa-onnx release archive for a platform ("" when there is none).
func runtimeAsset(goos, goarch string) (string, int64) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64":
		return "osx-universal2-shared", 43_600_000
	case "linux/amd64":
		return "linux-x64-shared", 28_200_000
	case "linux/arm64":
		return "linux-aarch64-shared-cpu", 28_100_000
	case "windows/amd64":
		return "win-x64-shared-MD-Release", 20_500_000
	case "windows/arm64":
		return "win-arm64-shared-MD-Release", 19_700_000
	}
	return "", 0
}

func runtimeModel() LocalModel {
	m := LocalModel{Provider: ProviderRuntime, ID: "sherpa-onnx", DisplayName: "sherpa-onnx runtime " + SherpaVersion,
		Description: "Runs Parakeet, Whisper and Piper on this machine (ONNX Runtime, CPU)", Required: []string{SherpaOffline, SherpaOfflineTTS}}
	asset, size := runtimeAsset(runtime.GOOS, runtime.GOARCH)
	if asset == "" {
		return m
	}
	name := "sherpa-onnx-" + SherpaVersion + "-" + asset + ".tar.bz2"
	m.Files = []RemoteFile{{URL: releases + SherpaVersion + "/" + name, Archive: true, Size: size, Keep: keepRuntime}}
	return m
}

// keepRuntime extracts the two command line tools and the shared libraries they load.
func keepRuntime(rel string) bool {
	if rel == SherpaOffline || rel == SherpaOfflineTTS || strings.HasPrefix(rel, "lib/") {
		return true
	}
	if strings.HasPrefix(rel, "bin/") && strings.Count(rel, "/") == 1 {
		b := strings.ToLower(rel)
		return strings.HasSuffix(b, ".dll") || strings.HasSuffix(b, ".dylib") || strings.Contains(b, ".so")
	}
	return false
}

// keepOnly extracts the named top-level files.
func keepOnly(names ...string) func(string) bool {
	return func(rel string) bool { return slices.Contains(names, rel) }
}

func whisperModel(id, name, desc string, size int64, rec bool) LocalModel {
	files := []string{id + "-encoder.int8.onnx", id + "-decoder.int8.onnx", id + "-tokens.txt"}
	return LocalModel{Provider: ProviderWhisper, ID: id, DisplayName: name, Description: desc, Kind: KindSTT, Recommended: rec, Needs: []string{RuntimeKey},
		Files:    []RemoteFile{{URL: asrBase + "sherpa-onnx-whisper-" + id + ".tar.bz2", Archive: true, Size: size, Keep: keepOnly(files...)}},
		Required: files}
}

func parakeetModel(id, archive, name, desc string, size int64, langs []string, rec bool) LocalModel {
	files := []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}
	return LocalModel{Provider: ProviderParakeet, ID: id, DisplayName: name, Description: desc, Kind: KindSTT, Languages: langs, Recommended: rec, Needs: []string{RuntimeKey},
		Files:    []RemoteFile{{URL: asrBase + archive + ".tar.bz2", Archive: true, Size: size, Keep: keepOnly(files...)}},
		Required: files}
}

// piperVoice is a Piper voice as converted for sherpa-onnx (id "<lang>_<REGION>-<name>-<quality>"); its archive
// carries the espeak-ng data the voice needs for phonemes.
func piperVoice(id, desc string, size int64, rec bool) LocalModel {
	parts := strings.Split(id, "-")
	locale, name, quality := parts[0], parts[1], parts[2]
	lang := strings.ReplaceAll(locale, "_", "-")
	return LocalModel{Provider: ProviderPiper, ID: id, DisplayName: titleCase(name) + " (" + lang + ", " + quality + ")", Description: desc, Kind: KindTTS,
		Languages: []string{lang}, Recommended: rec, Needs: []string{RuntimeKey},
		Files: []RemoteFile{{URL: ttsBase + "vits-piper-" + id + ".tar.bz2", Archive: true, Size: size, Keep: func(rel string) bool {
			return rel == id+".onnx" || rel == "tokens.txt" || strings.HasPrefix(rel, "espeak-ng-data/")
		}}},
		Required: []string{id + ".onnx", "tokens.txt", "espeak-ng-data"}}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var europe25 = []string{"bg", "hr", "cs", "da", "nl", "en", "et", "fi", "fr", "de", "el", "hu", "it", "lv", "lt", "mt", "pl", "pt", "ro", "sk", "sl", "es", "sv", "ru", "uk"}

// DefaultCatalog lists the runtime and the downloadable models: Parakeet and Whisper (sherpa-onnx int8 exports) and
// a set of Piper voices. Sizes are approximate (the archives' sizes; only the files the engine needs are kept).
func DefaultCatalog() []LocalModel {
	return []LocalModel{
		runtimeModel(),
		parakeetModel("parakeet-tdt-0.6b-v3", "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8", "Parakeet TDT 0.6B v3", "NVIDIA Parakeet, 25 European languages, fast on CPU", 465_000_000, europe25, true),
		parakeetModel("parakeet-tdt-0.6b-v2", "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", "Parakeet TDT 0.6B v2", "NVIDIA Parakeet, English only", 460_000_000, []string{"en"}, false),
		whisperModel("tiny", "Whisper tiny", "Fastest, rough accuracy, 99 languages", 111_000_000, false),
		whisperModel("base", "Whisper base", "Fast, good for dictation, 99 languages", 199_000_000, true),
		whisperModel("small", "Whisper small", "Better accuracy, slower", 610_000_000, false),
		whisperModel("turbo", "Whisper large v3 turbo", "Best accuracy, needs a fast machine", 1_100_000_000, false),
		piperVoice("en_US-lessac-medium", "US English, neutral", 64_000_000, true),
		piperVoice("en_US-amy-medium", "US English, female", 64_000_000, false),
		piperVoice("en_US-ryan-high", "US English, male, high quality", 115_000_000, false),
		piperVoice("en_GB-alan-medium", "British English, male", 64_000_000, false),
		piperVoice("de_DE-thorsten-medium", "German, male", 64_000_000, false),
		piperVoice("fr_FR-siwis-medium", "French, female", 64_000_000, false),
		piperVoice("es_ES-davefx-medium", "Spanish, male", 64_000_000, false),
		piperVoice("it_IT-paola-medium", "Italian, female", 64_000_000, false),
		piperVoice("pt_BR-faber-medium", "Brazilian Portuguese, male", 64_000_000, false),
		piperVoice("ru_RU-irina-medium", "Russian, female", 64_000_000, false),
		piperVoice("zh_CN-huayan-medium", "Mandarin Chinese, female", 64_000_000, false),
	}
}

// Models stores the runtime and the model files under Dir (<Dir>/<provider>/<id>/) and runs their downloads.
type Models struct {
	Dir     string
	Catalog []LocalModel
	HTTP    *http.Client

	mu      sync.Mutex
	running map[string]*Download
}

// NewModels creates the store; catalog nil means DefaultCatalog.
func NewModels(dir string, catalog []LocalModel, hc *http.Client) *Models {
	if catalog == nil {
		catalog = DefaultCatalog()
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Models{Dir: dir, Catalog: catalog, HTTP: hc, running: map[string]*Download{}}
}

// Find returns a catalog entry by key ("<provider>/<id>").
func (m *Models) Find(key string) (LocalModel, bool) {
	for _, x := range m.Catalog {
		if x.Key() == key {
			return x, true
		}
	}
	return LocalModel{}, false
}

// Of lists the catalog models of a provider.
func (m *Models) Of(provider string) []LocalModel {
	var out []LocalModel
	for _, x := range m.Catalog {
		if x.Provider == provider {
			out = append(out, x)
		}
	}
	return out
}

// Path is the directory of a model.
func (m *Models) Path(x LocalModel) string { return filepath.Join(m.Dir, x.Provider, x.ID) }

// Installed reports whether every required path exists, and the size of the model's directory.
func (m *Models) Installed(x LocalModel) (bool, int64) {
	if len(x.Required) == 0 {
		return false, 0
	}
	for _, f := range x.Required {
		if _, err := os.Stat(filepath.Join(m.Path(x), filepath.FromSlash(f))); err != nil {
			return false, 0
		}
	}
	var size int64
	_ = filepath.WalkDir(m.Path(x), func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return true, size
}

// Ready reports whether a model and everything it needs are installed.
func (m *Models) Ready(x LocalModel) bool {
	if ok, _ := m.Installed(x); !ok {
		return false
	}
	for _, k := range x.Needs {
		if n, found := m.Find(k); !found || !m.Ready(n) {
			return false
		}
	}
	return true
}

// Downloading returns the running download of a model, nil when none.
func (m *Models) Downloading(key string) *Download {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[key]
}

// Download is a running (or finished) model download.
type Download struct {
	Model      LocalModel
	downloaded atomic.Int64
	total      atomic.Int64
	done       chan struct{}
	err        error
	cancel     context.CancelFunc
}

// Progress is the bytes downloaded so far and the expected total (0 when unknown).
func (d *Download) Progress() (int64, int64) { return d.downloaded.Load(), d.total.Load() }

// Done is closed when the download ended; Err is its outcome then.
func (d *Download) Done() <-chan struct{} { return d.done }

// Err is the download's error (nil on success); valid after Done.
func (d *Download) Err() error { return d.err }

// ErrUnknownModel is returned for a key not in the catalog.
var ErrUnknownModel = errors.New("unknown speech model")

// Start starts downloading a model (and what it needs that is missing) in the background, or returns the running
// download. ctx bounds the download, not the caller's request. A ready model returns a finished download.
func (m *Models) Start(ctx context.Context, key string) (*Download, error) {
	x, ok := m.Find(key)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownModel, key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.running[key]; d != nil {
		return d, nil
	}
	d := &Download{Model: x, done: make(chan struct{})}
	plan := m.plan(x)
	if len(plan) == 0 {
		_, size := m.Installed(x)
		d.downloaded.Store(size)
		d.total.Store(size)
		close(d.done)
		return d, nil
	}
	for _, p := range plan {
		if len(p.Files) == 0 {
			return nil, fmt.Errorf("%s is not available for %s/%s", p.DisplayName, runtime.GOOS, runtime.GOARCH)
		}
	}
	var total int64
	for _, p := range plan {
		total += p.Size()
	}
	d.total.Store(total)
	ctx, d.cancel = context.WithCancel(ctx)
	m.running[key] = d
	go func() {
		d.err = m.fetchAll(ctx, d, plan)
		d.cancel()
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
		close(d.done)
	}()
	return d, nil
}

// plan lists what to download for x: its missing needs first, then x itself when missing.
func (m *Models) plan(x LocalModel) []LocalModel {
	var out []LocalModel
	for _, k := range x.Needs {
		if n, ok := m.Find(k); ok {
			out = append(out, m.plan(n)...)
		}
	}
	if ok, _ := m.Installed(x); !ok {
		out = append(out, x)
	}
	return out
}

// Delete stops a running download of the model and removes its files; cancelled reports a stopped download.
func (m *Models) Delete(key string) (cancelled bool, err error) {
	x, ok := m.Find(key)
	if !ok {
		return false, fmt.Errorf("%w %q", ErrUnknownModel, key)
	}
	if d := m.Downloading(key); d != nil {
		d.cancel()
		<-d.done
		cancelled = true
	}
	return cancelled, os.RemoveAll(m.Path(x))
}

func (m *Models) fetchAll(ctx context.Context, d *Download, plan []LocalModel) error {
	var estimates int64
	for _, p := range plan {
		estimates += p.Size()
	}
	for _, p := range plan {
		if ok, _ := m.Installed(p); ok {
			continue // another download installed it meanwhile
		}
		estimates -= p.Size()
		if err := m.fetch(ctx, d, p, estimates); err != nil {
			return err
		}
	}
	return nil
}

// fetch downloads a model's files into a temporary directory next to the model's and moves it into place, so an
// existing model directory is always complete.
func (m *Models) fetch(ctx context.Context, d *Download, x LocalModel, laterEstimates int64) error {
	parent := filepath.Join(m.Dir, x.Provider)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+x.ID+".download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	rest := x.Size()
	for _, f := range x.Files {
		rest -= f.Size
		if err := m.fetchFile(ctx, d, f, tmp, rest+laterEstimates); err != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			return fmt.Errorf("downloading %s: %w", x.DisplayName, err)
		}
	}
	for _, r := range x.Required {
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(r))); err != nil {
			return fmt.Errorf("downloading %s: %s is missing from the download", x.DisplayName, r)
		}
	}
	final := m.Path(x)
	_ = os.RemoveAll(final)
	return os.Rename(tmp, final)
}

// countingReader adds the bytes read to a download's progress.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (m *Models) fetchFile(ctx context.Context, d *Download, f RemoteFile, dir string, laterEstimates int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	res, err := m.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered HTTP %d", hostOf(f.URL), res.StatusCode)
	}
	size := f.Size
	if res.ContentLength > 0 {
		size = res.ContentLength
	}
	d.total.Store(d.downloaded.Load() + size + laterEstimates)
	body := countingReader{r: res.Body, n: &d.downloaded}
	if f.Archive {
		keep := f.Keep
		if keep == nil {
			keep = func(rel string) bool { return !strings.Contains(rel, "/") }
		}
		return extractTarBz2(ctx, body, dir, keep)
	}
	return writeFile(filepath.Join(dir, f.Name), body, 0o644)
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extractTarBz2 strips the archive's root folder and writes the entries keep accepts into dir: regular files
// (executable ones stay executable) and symlinks to a sibling file (shared library aliases). Paths that would
// leave dir are skipped.
func extractTarBz2(ctx context.Context, r io.Reader, dir string, keep func(string) bool) error {
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if strings.HasPrefix(name, "/") || name == "." || slices.Contains(strings.Split(name, "/"), "..") {
			continue
		}
		rel := name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			rel = name[i+1:]
		} else if h.Typeflag == tar.TypeDir {
			continue
		}
		if rel == "" || !keep(rel) {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := writeFile(dst, tr, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if strings.ContainsAny(h.Linkname, `/\`) || h.Linkname == ".." {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return err
			}
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		}
	}
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.SplitN(u, "/", 2)[0]
}

// ModelState is a catalog entry with its local state.
type ModelState struct {
	LocalModel
	Installed   bool
	SizeBytes   int64
	Downloading bool
}

// States lists the catalog (of one kind; 0: everything, the runtime included) with the local state.
func (m *Models) States(kind Kind) []ModelState {
	var out []ModelState
	for _, x := range m.Catalog {
		if kind != 0 && x.Kind != kind {
			continue
		}
		ok, size := m.Installed(x)
		if !ok {
			size = x.Size()
		}
		out = append(out, ModelState{LocalModel: x, Installed: ok, SizeBytes: size, Downloading: m.Downloading(x.Key()) != nil})
	}
	return out
}

// pollInterval is how often Watch reports progress.
var pollInterval = 250 * time.Millisecond

// Watch calls fn with the progress until the download ends, then returns its error. Cancelling ctx stops
// watching, not the download.
func (d *Download) Watch(ctx context.Context, fn func(downloaded, total int64) error) error {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		n, total := d.Progress()
		if err := fn(n, total); err != nil {
			return err
		}
		select {
		case <-d.done:
			return d.err
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
