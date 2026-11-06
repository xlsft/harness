package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSiteOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Example.com/a?b":   "example.com",
		"http://docs.example.com:8080":  "docs.example.com",
		"about:blank":                   "",
		"chrome-error://chromewebdata/": "",
		"file:///etc/passwd":            "",
		"not a url":                     "",
	} {
		if got := SiteOf(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestSiteGrantsPersistOnThisDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser", "real-chrome-sites.json")
	g := NewSiteGrants(path)
	if g.Has("example.com") {
		t.Fatal("granted out of nothing")
	}
	if err := g.Add("example.com"); err != nil {
		t.Fatal(err)
	}
	if err := g.Add("a.test"); err != nil {
		t.Fatal(err)
	}
	again := NewSiteGrants(path)
	if !again.Has("example.com") || strings.Join(again.List(), ",") != "a.test,example.com" {
		t.Errorf("reloaded: %v", again.List())
	}
	if again.Has("sub.example.com") {
		t.Error("a subdomain is a site of its own")
	}
	if err := again.Remove("example.com"); err != nil {
		t.Fatal(err)
	}
	if NewSiteGrants(path).Has("example.com") {
		t.Error("the revocation did not stay")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", st, err)
	}
	// A broken file is no grants, not a failure.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if NewSiteGrants(path).Has("a.test") {
		t.Error("read a broken file")
	}
}

func TestFindEndpoint(t *testing.T) {
	ctx := context.Background()
	t.Setenv(realChromeEnv, "")
	dir := t.TempDir()
	if _, err := findEndpoint(ctx, []string{dir}); !errors.Is(err, ErrRealChromeNotFound) {
		t.Fatalf("no file: %v", err)
	}
	// A port nothing listens on: Chrome left the file behind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ln.Close()
	write(fmt.Sprintf("%d\n/devtools/browser/abc\n", port))
	if _, err := findEndpoint(ctx, []string{dir}); !errors.Is(err, ErrRealChromeNotFound) {
		t.Fatalf("stale file: %v", err)
	}
	ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("the port went elsewhere: %v", err)
	}
	defer ln.Close()
	got, err := findEndpoint(ctx, []string{dir})
	if err != nil || got != fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/abc", port) {
		t.Fatalf("%q %v", got, err)
	}
	write(fmt.Sprintf("%d\n/somewhere/else\n", port))
	if _, err := findEndpoint(ctx, []string{dir}); !errors.Is(err, ErrRealChromeNotFound) {
		t.Fatalf("odd path: %v", err)
	}
	// The override is for this machine only, and so is what it answers.
	t.Setenv(realChromeEnv, "192.0.2.1:9222")
	if _, err := findEndpoint(ctx, nil); err == nil || !strings.Contains(err.Error(), "this machine") {
		t.Fatalf("remote override: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://192.0.2.1/devtools/browser/x"}`)
	}))
	defer srv.Close()
	t.Setenv(realChromeEnv, strings.TrimPrefix(srv.URL, "http://"))
	if _, err := findEndpoint(ctx, nil); err == nil || !strings.Contains(err.Error(), "off this machine") {
		t.Fatalf("remote answer: %v", err)
	}
}

func jpegFrame(t *testing.T, c color.Color, box image.Rectangle) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if (image.Point{x, y}).In(box) {
				img.Set(x, y, c)
			} else {
				img.Set(x, y, color.White)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEncodeGIFDrawsOnlyWhatChanged(t *testing.T) {
	start := time.Unix(1000, 0)
	box := image.Rect(10, 10, 30, 30)
	frames := []recordedFrame{
		{at: start, jpg: jpegFrame(t, color.Black, box)},
		{at: start.Add(500 * time.Millisecond), jpg: jpegFrame(t, color.Black, box)}, // nothing moved
		{at: start.Add(time.Second), jpg: jpegFrame(t, color.RGBA{255, 0, 0, 255}, image.Rect(100, 50, 120, 70))},
		{at: start.Add(2 * time.Second), jpg: []byte("not a jpeg")},
	}
	var out bytes.Buffer
	if err := encodeGIF(&out, frames); err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(&out)
	if err != nil {
		t.Fatal(err)
	}
	if g.Config.Width != 200 || g.Config.Height != 100 {
		t.Errorf("canvas %dx%d", g.Config.Width, g.Config.Height)
	}
	if len(g.Image) != 2 {
		t.Fatalf("%d frames, want 2: the still frame extends the first", len(g.Image))
	}
	if g.Delay[0] != 100 {
		t.Errorf("first frame lasts %d cs, want 100: its own 50 and the still frame's", g.Delay[0])
	}
	if b := g.Image[1].Bounds(); b.Dx() >= 200 || b.Dy() >= 100 {
		t.Errorf("the second frame redraws everything: %v", b)
	}
	var none bytes.Buffer
	if err := encodeGIF(&none, []recordedFrame{{at: start, jpg: []byte("broken")}}); err == nil {
		t.Error("a recording of nothing encoded")
	}
}

func TestRecorderLimits(t *testing.T) {
	now := time.Unix(1000, 0)
	r := &recorder{started: now}
	r.offer([]byte{1}, now)
	r.offer([]byte{2}, now.Add(time.Millisecond)) // too soon
	if len(r.frames) != 1 {
		t.Fatalf("%d frames", len(r.frames))
	}
	r.offer([]byte{3}, now.Add(recordingMaxDuration+time.Second))
	r.offer([]byte{4}, now.Add(recordingMaxDuration+2*time.Second))
	if len(r.frames) != 1 || !r.full {
		t.Errorf("past the limit: %d frames, full %v", len(r.frames), r.full)
	}
}

// Live tests: a throwaway headless Chromium with a temporary profile stands in for the user's Chrome, with remote debugging on.
// The user's actual Chrome and profile are never touched: ChromeDirs names only the temporary folder.

type fakeUserChrome struct {
	dir  string
	cmd  *exec.Cmd
	done chan struct{}
}

func startFakeUserChrome(t *testing.T, firstPage string) *fakeUserChrome {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the live browser test in -short mode")
	}
	t.Setenv(realChromeEnv, "")
	dir := t.TempDir()
	exe, ok := Discover(dir)
	if !ok {
		t.Skip("no Chromium-based browser installed")
	}
	profile := filepath.Join(dir, "profile")
	cmd := exec.Command(exe.Path, "--headless=new", "--remote-debugging-port=0", "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--disable-gpu", firstPage)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &fakeUserChrome{dir: profile, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(f.done) }()
	t.Cleanup(func() {
		killProcessGroup(cmd)
		<-f.done
	})
	waitUntil(t, "Chrome to write its port", func() bool {
		_, err := os.Stat(filepath.Join(profile, "DevToolsActivePort"))
		return err == nil
	})
	return f
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pageTargets asks Chrome itself which pages are open.
func (f *fakeUserChrome) pageTargets(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "DevToolsActivePort"))
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(string(data), "\n")[0]
	res, err := http.Get("http://127.0.0.1:" + port + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var list []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range list {
		if p.Type == "page" {
			out = append(out, p.URL)
		}
	}
	return out
}

type siteAsks struct {
	mu   sync.Mutex
	list []SiteRequest
}

func (a *siteAsks) add(_ string, r SiteRequest) {
	a.mu.Lock()
	a.list = append(a.list, r)
	a.mu.Unlock()
}

func (a *siteAsks) last() (SiteRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.list) == 0 {
		return SiteRequest{}, false
	}
	return a.list[len(a.list)-1], true
}

func TestLiveRealChromeKeepsToGrantedSites(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.Host+r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>Page %s</title><a id="out" href="%s">out</a>`, r.URL.Path, r.URL.Query().Get("out"))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	granted := fmt.Sprintf("http://127.0.0.1:%d", port)
	other := fmt.Sprintf("http://localhost:%d", port) // another site, the same server

	userTab := granted + "/users-own-tab"
	chrome := startFakeUserChrome(t, userTab)
	waitUntil(t, "the user's tab", func() bool { return len(chrome.pageTargets(t)) > 0 })

	asks := &siteAsks{}
	m := &Manager{Dir: t.TempDir(), IdleTimeout: -1, ChromeDirs: []string{chrome.dir}, OnSiteRequest: asks.add}
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if !m.RealChromeAvailable(ctx) {
		t.Fatal("the running Chrome was not found")
	}
	b, err := m.Session("s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.UseRealChrome(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !b.State().RealChrome {
		t.Error("the state does not say so")
	}

	// The user's own tab is not among those the client holds.
	for _, tab := range b.State().Tabs {
		if strings.Contains(tab.Url, "users-own-tab") {
			t.Fatal("attached to the user's own tab")
		}
	}

	// A site without a grant waits for the user; no is no, and nothing was loaded.
	navErr := make(chan error, 1)
	go func() {
		_, err := b.Navigate(ctx, granted+"/one?out="+other+"/two", true)
		navErr <- err
	}()
	waitUntil(t, "the question", func() bool { _, ok := asks.last(); return ok })
	req, _ := asks.last()
	if req.Site != "127.0.0.1" || !strings.Contains(req.URL, "/one") {
		t.Errorf("asked about %+v", req)
	}
	if !b.AnswerSite(req.ID, SiteDeny) {
		t.Fatal("nothing waited for the answer")
	}
	if err := <-navErr; !errors.Is(err, ErrSiteDenied) {
		t.Fatalf("after no: %v", err)
	}
	mu.Lock()
	loaded := hits[strings.TrimPrefix(granted, "http://")+"/one"]
	mu.Unlock()
	if loaded != 0 {
		t.Error("a refused site was loaded")
	}

	// Yes for the session lets it through.
	go func() {
		_, err := b.Navigate(ctx, granted+"/one?out="+other+"/two", true)
		navErr <- err
	}()
	waitUntil(t, "the second question", func() bool { r, ok := asks.last(); return ok && r.ID != req.ID })
	req, _ = asks.last()
	if !b.AnswerSite(req.ID, SiteAllowSession) {
		t.Fatal("nothing waited for the second answer")
	}
	if err := <-navErr; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.CurrentURL(), "/one") {
		t.Fatalf("at %q", b.CurrentURL())
	}
	if m.Grants().Has("127.0.0.1") {
		t.Error("a session's yes was stored for good")
	}

	// A page's own way out (here, script following a link) stops at the guard and asks.
	before := len(asks.list)
	tab, c, err := b.activeTab(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var res struct{}
	if err := c.Call(ctx, tab.session, "Runtime.evaluate", map[string]any{"expression": "document.getElementById('out').click()"}, &res); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the question about the link", func() bool {
		asks.mu.Lock()
		defer asks.mu.Unlock()
		return len(asks.list) > before
	})
	req, _ = asks.last()
	if req.Site != "localhost" {
		t.Errorf("asked about %+v", req)
	}
	mu.Lock()
	reached := hits[strings.TrimPrefix(other, "http://")+"/two"]
	mu.Unlock()
	if reached != 0 {
		t.Error("the guard let a page leave for a site without a grant")
	}
	// Yes for good is stored, and the tab goes where it was going.
	if !b.AnswerSite(req.ID, SiteAllowAlways) {
		t.Fatal("nothing waited for the third answer")
	}
	if !m.Grants().Has("localhost") {
		t.Error("not stored")
	}
	waitUntil(t, "the tab to arrive", func() bool { return strings.Contains(b.CurrentURL(), "/two") })

	// What would touch the whole profile is refused.
	if err := b.ClearData(ctx); err == nil {
		t.Error("ClearData ran on the user's Chrome")
	}

	// Leaving closes the client's tabs and leaves Chrome and the user's tab alone.
	if err := b.UseRealChrome(ctx, false); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the client's tabs to close", func() bool {
		pages := chrome.pageTargets(t)
		return len(pages) == 1 && strings.Contains(pages[0], "users-own-tab")
	})
	select {
	case <-chrome.done:
		t.Fatal("Chrome went down with the client")
	default:
	}
}

func TestLiveRealChromeRefusedWithoutOne(t *testing.T) {
	t.Setenv(realChromeEnv, "")
	m := &Manager{Dir: t.TempDir(), IdleTimeout: -1, ChromeDirs: []string{t.TempDir()}}
	t.Cleanup(m.Close)
	b, err := m.Session("s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.UseRealChrome(context.Background(), true); !errors.Is(err, ErrRealChromeNotFound) {
		t.Fatalf("%v", err)
	}
	if b.Attached() {
		t.Error("attached to nothing")
	}
}

func TestLiveRecording(t *testing.T) {
	m, _ := liveManager(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body style="margin:0"><div id="box" style="width:100px;height:100px;background:red"></div>
<script>let i=0;setInterval(()=>{document.getElementById('box').style.background=(i++%2)?'red':'blue'},50)</script>`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	b, err := m.Session("rec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.StopRecording(t.TempDir()); err == nil {
		t.Error("stopped a recording that never ran")
	}
	if _, err := b.Navigate(ctx, srv.URL, true); err != nil {
		t.Fatal(err)
	}
	if err := b.StartRecording(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.StartRecording(ctx); err == nil {
		t.Error("two recordings at once")
	}
	if !b.State().Recording {
		t.Error("the state does not say so")
	}
	waitUntil(t, "frames to arrive", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.rec != nil && len(b.rec.frames) >= 4
	})
	dir := t.TempDir()
	rec, err := b.StopRecording(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Frames < 4 || rec.Size == 0 {
		t.Errorf("%+v", rec)
	}
	f, err := os.Open(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) < 2 {
		t.Errorf("%d frames in the file", len(g.Image))
	}
	if st := b.State(); st.Recording || st.LastRecording != rec.Path {
		t.Errorf("state: %+v", st)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("left behind: %v %v", entries, err)
	}
}
