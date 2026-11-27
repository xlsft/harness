package cloudrunner

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine is enough of the Docker Engine API to check what the driver sends.
type fakeEngine struct {
	mu       sync.Mutex
	networks map[string]map[string]any
	created  []map[string]any
	names    []string
	archives map[string][]tarFile
	deleted  []string
	queries  map[string]string
	pullErr  string
	noImage  bool
}

type tarFile struct {
	name     string
	mode     int64
	uid, gid int
	data     string
	dir      string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{networks: map[string]map[string]any{}, archives: map[string][]tarFile{}, queries: map[string]string{}}
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v1.41")
	f.queries[r.Method+" "+p] = r.URL.RawQuery
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
	switch {
	case p == "/_ping":
		_, _ = io.WriteString(w, "OK")
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/networks/"):
		n, ok := f.networks[strings.TrimPrefix(p, "/networks/")]
		if !ok {
			fail(404, "network harness-cloud not found")
			return
		}
		_ = json.NewEncoder(w).Encode(n)
	case r.Method == http.MethodPost && p == "/networks/create":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := map[string]any{"Id": "net1", "Name": in["Name"], "Internal": in["Internal"], "IPAM": map[string]any{"Config": []any{map[string]any{"Subnet": "172.30.0.0/16", "Gateway": "172.30.0.1"}}}}
		f.networks["net1"], f.networks[in["Name"].(string)] = n, n
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "net1"})
	case r.Method == http.MethodPost && p == "/containers/create":
		if f.noImage {
			fail(404, "No such image: harness-agent:test")
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.created = append(f.created, in)
		f.names = append(f.names, r.URL.Query().Get("name"))
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "c-new"})
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/archive"):
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(tr)
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/containers/"), "/archive")
			f.archives[id] = append(f.archives[id], tarFile{h.Name, h.Mode, h.Uid, h.Gid, string(b), r.URL.Query().Get("path")})
		}
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/containers/"):
		id := strings.TrimPrefix(p, "/containers/")
		if id == "gone" {
			fail(404, "No such container: gone")
			return
		}
		f.deleted = append(f.deleted, id)
		w.WriteHeader(204)
	case r.Method == http.MethodGet && p == "/containers/json":
		size := int64(4096)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "c1", "Names": []string{"/harness-cls_1"}, "Labels": map[string]string{LabelSession: "cls_1"}, "State": "running", "Created": 1700000000,
			"SizeRw": size, "NetworkSettings": map[string]any{"Networks": map[string]any{"harness-cloud": map[string]string{"IPAddress": "172.30.0.5"}}},
		}})
	case r.Method == http.MethodPost && p == "/images/create":
		_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n")
		if f.pullErr != "" {
			_, _ = io.WriteString(w, `{"error":"`+f.pullErr+`"}`+"\n")
		}
	case r.Method == http.MethodPost && (strings.HasSuffix(p, "/start") || strings.HasSuffix(p, "/stop")):
		w.WriteHeader(204)
	default:
		fail(500, "unexpected "+r.Method+" "+p)
	}
}

func TestDockerDriver(t *testing.T) {
	f := newFakeEngine()
	srv := httptest.NewServer(f)
	defer srv.Close()
	d, err := NewDocker(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := d.EnsureNetwork(ctx, "harness-cloud", true, nil)
	if err != nil || n.Gateway != "172.30.0.1" || !n.Internal {
		t.Fatalf("network %+v %v", n, err)
	}
	if again, err := d.EnsureNetwork(ctx, "harness-cloud", true, nil); err != nil || again.ID != "net1" {
		t.Fatalf("existing network %+v %v", again, err)
	}
	if _, err := d.EnsureNetwork(ctx, "harness-cloud", false, nil); err == nil {
		t.Fatal("a network that is not internal must be refused")
	}

	id, err := d.Create(ctx, ContainerSpec{
		Name: "harness-cls_1", Image: "harness-agent:test", Labels: map[string]string{LabelSession: "cls_1"}, Env: []string{"A=1"}, User: "10001:10001",
		Runtime: "runsc", CPUMillis: 1500, MemoryBytes: 1 << 30, Pids: 512, DiskBytes: 5 << 30, Network: "harness-cloud",
		Mounts: []Mount{{Source: "/var/cache/x/pnpm", Target: "/cache/pnpm"}},
	})
	if err != nil || id != "c-new" {
		t.Fatalf("create %q %v", id, err)
	}
	in := f.created[0]
	host := in["HostConfig"].(map[string]any)
	checks := map[string]any{"Runtime": "runsc", "NanoCpus": 1.5e9, "Memory": float64(1 << 30), "MemorySwap": float64(1 << 30), "PidsLimit": 512.0, "NetworkMode": "harness-cloud", "Init": true}
	for k, want := range checks {
		if host[k] != want {
			t.Errorf("HostConfig.%s = %v, want %v", k, host[k], want)
		}
	}
	if caps := host["CapDrop"].([]any); len(caps) != 1 || caps[0] != "ALL" {
		t.Errorf("CapDrop %v", caps)
	}
	if so := host["SecurityOpt"].([]any); so[0] != "no-new-privileges:true" {
		t.Errorf("SecurityOpt %v", so)
	}
	if host["StorageOpt"].(map[string]any)["size"] != "5368709120" {
		t.Errorf("StorageOpt %v", host["StorageOpt"])
	}
	if _, ok := host["Privileged"]; ok {
		t.Error("never privileged")
	}
	if in["User"] != "10001:10001" || f.names[0] != "harness-cls_1" {
		t.Errorf("user %v name %v", in["User"], f.names[0])
	}
	if _, ok := in["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)["harness-cloud"]; !ok {
		t.Error("the container joins only the network it was given")
	}

	if err := d.PutFiles(ctx, "c-new", []File{{Path: "/run/harness/token", Mode: 0o600, UID: 10001, GID: 10001, Data: []byte("hdt_x")}}); err != nil {
		t.Fatal(err)
	}
	got := f.archives["c-new"]
	if len(got) != 1 || got[0].name != "token" || got[0].dir != "/run/harness" || got[0].mode != 0o600 || got[0].uid != 10001 || got[0].data != "hdt_x" {
		t.Fatalf("archive %+v", got)
	}

	boxes, err := d.List(ctx, map[string]string{LabelRunner: "ahs_1"}, true)
	if err != nil || len(boxes) != 1 {
		t.Fatalf("list %v %v", boxes, err)
	}
	if b := boxes[0]; b.ID != "c1" || !b.Running || b.SizeRw != 4096 || b.IPs["harness-cloud"] != "172.30.0.5" || b.Name != "harness-cls_1" {
		t.Errorf("container %+v", b)
	}
	if q := f.queries["GET /containers/json"]; !strings.Contains(q, "size=1") || !strings.Contains(q, "dev.harness.runner%3Dahs_1") {
		t.Errorf("list query %s", q)
	}

	if err := d.Remove(ctx, "c1"); err != nil || f.deleted[0] != "c1" {
		t.Fatal(err)
	}
	if q := f.queries["DELETE /containers/c1"]; !strings.Contains(q, "force=1") || !strings.Contains(q, "v=1") {
		t.Errorf("remove query %s", q)
	}
	if err := d.Remove(ctx, "gone"); err != nil {
		t.Errorf("removing what is gone is done: %v", err)
	}

	f.noImage = true
	if _, err := d.Create(ctx, ContainerSpec{Image: "harness-agent:test"}); !errors.Is(err, ErrNoSuchImage) {
		t.Errorf("missing image: %v", err)
	}
	if err := d.Pull(ctx, "ghcr.io/acme/agent:1"); err != nil {
		t.Fatal(err)
	}
	if q := f.queries["POST /images/create"]; !strings.Contains(q, "fromImage=ghcr.io%2Facme%2Fagent") || !strings.Contains(q, "tag=1") {
		t.Errorf("pull query %s", q)
	}
	f.pullErr = "denied"
	if err := d.Pull(ctx, "x"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("a pull error in the stream: %v", err)
	}
}

func TestDockerOverUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "hcr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	srv := &http.Server{Handler: newFakeEngine(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	d, err := NewDocker("unix://"+sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDocker("ftp://x", nil); err == nil {
		t.Error("an unknown scheme is refused")
	}
}

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][2]string{
		"harness-agent":             {"harness-agent", "latest"},
		"harness-agent:1":           {"harness-agent", "1"},
		"localhost:5000/agent":      {"localhost:5000/agent", "latest"},
		"localhost:5000/agent:2":    {"localhost:5000/agent", "2"},
		"ghcr.io/a/b@sha256:abcdef": {"ghcr.io/a/b@sha256:abcdef", ""},
	} {
		n, tag := splitImage(in)
		if n != want[0] || tag != want[1] {
			t.Errorf("%s: %s %s", in, n, tag)
		}
	}
}
