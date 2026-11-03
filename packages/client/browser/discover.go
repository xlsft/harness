package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Source says where the browser executable comes from.
type Source int

// Executable sources.
const (
	SourceNone Source = iota
	// SourceEnv: HARNESS_BROWSER names it.
	SourceEnv
	// SourceSystem: an installed Chrome, Chromium, Edge or Brave.
	SourceSystem
	// SourceDownloaded: the Chrome for Testing build in <dir>/chrome-<version>.
	SourceDownloaded
)

// Executable is a Chromium-based browser that can run headless.
type Executable struct {
	Path    string
	Product string
	Source  Source
}

// candidate is a known install location.
type candidate struct {
	product string
	paths   []string
}

func systemCandidates(goos string) []candidate {
	home, _ := os.UserHomeDir()
	switch goos {
	case "darwin":
		app := func(name, bin string) []string {
			rel := filepath.Join(name+".app", "Contents", "MacOS", bin)
			out := []string{filepath.Join("/Applications", rel)}
			if home != "" {
				out = append(out, filepath.Join(home, "Applications", rel))
			}
			return out
		}
		return []candidate{
			{"Google Chrome", app("Google Chrome", "Google Chrome")},
			{"Chromium", app("Chromium", "Chromium")},
			{"Microsoft Edge", app("Microsoft Edge", "Microsoft Edge")},
			{"Brave", app("Brave Browser", "Brave Browser")},
			{"Google Chrome Canary", app("Google Chrome Canary", "Google Chrome Canary")},
		}
	case "windows":
		var roots []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if v := os.Getenv(env); v != "" {
				roots = append(roots, v)
			}
		}
		under := func(rel ...string) []string {
			var out []string
			for _, r := range roots {
				out = append(out, filepath.Join(append([]string{r}, rel...)...))
			}
			return out
		}
		return []candidate{
			{"Google Chrome", under("Google", "Chrome", "Application", "chrome.exe")},
			{"Microsoft Edge", under("Microsoft", "Edge", "Application", "msedge.exe")},
			{"Brave", under("BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
			{"Chromium", under("Chromium", "Application", "chrome.exe")},
		}
	default:
		look := func(names ...string) []string {
			var out []string
			for _, n := range names {
				if p, err := exec.LookPath(n); err == nil {
					out = append(out, p)
				}
			}
			return out
		}
		return []candidate{
			{"Google Chrome", append(look("google-chrome", "google-chrome-stable"), "/opt/google/chrome/chrome")},
			{"Chromium", append(look("chromium", "chromium-browser"), "/snap/bin/chromium", "/usr/lib/chromium/chromium")},
			{"Microsoft Edge", look("microsoft-edge", "microsoft-edge-stable")},
			{"Brave", look("brave-browser", "brave")},
		}
	}
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode()&0o111 != 0
}

// Discover finds a browser: HARNESS_BROWSER, then the downloaded build under dir, then an installed one. The
// downloaded build wins over system browsers only when HARNESS_BROWSER_PREFER_DOWNLOADED=1; by default an
// installed browser is used and the download is the fallback.
func Discover(dir string) (Executable, bool) {
	if p := strings.TrimSpace(os.Getenv("HARNESS_BROWSER")); p != "" {
		if isExecutable(p) {
			return Executable{Path: p, Product: productName(p), Source: SourceEnv}, true
		}
	}
	downloaded, haveDownloaded := downloadedExecutable(dir)
	if haveDownloaded && os.Getenv("HARNESS_BROWSER_PREFER_DOWNLOADED") == "1" {
		return downloaded, true
	}
	for _, c := range systemCandidates(runtime.GOOS) {
		for _, p := range c.paths {
			if isExecutable(p) {
				return Executable{Path: p, Product: c.product, Source: SourceSystem}, true
			}
		}
	}
	if haveDownloaded {
		return downloaded, true
	}
	return Executable{}, false
}

func productName(p string) string {
	l := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(l, "edge"):
		return "Microsoft Edge"
	case strings.Contains(l, "brave"):
		return "Brave"
	case strings.Contains(l, "chromium"):
		return "Chromium"
	case strings.Contains(l, "for testing"):
		return "Chrome for Testing"
	}
	return "Google Chrome"
}
