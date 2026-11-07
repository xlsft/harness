package cloudrunner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Docker is the Docker Engine API (v1.41, which Podman's compatible service speaks too) over a unix socket or TCP.
type Docker struct {
	hc   *http.Client
	base string
}

// DefaultEngine is where the engine is when DOCKER_HOST doesn't say: Docker's socket, or Podman's rootful one.
func DefaultEngine() string {
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	for _, p := range []string{"/var/run/docker.sock", "/run/podman/podman.sock"} {
		if _, err := os.Stat(p); err == nil {
			return "unix://" + p
		}
	}
	return "unix:///var/run/docker.sock"
}

// NewDocker connects to an engine at an address like unix:///var/run/docker.sock, tcp://127.0.0.1:2375 or http://…; hc may
// be nil. A remote engine on plain TCP is unauthenticated: keep it on the runner's own machine.
func NewDocker(addr string, hc *http.Client) (*Docker, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("engine address %q: %w", addr, err)
	}
	base := ""
	switch u.Scheme {
	case "unix":
		socket := u.Path
		if hc == nil {
			hc = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socket)
			}}}
		}
		base = "http://engine"
	case "tcp":
		base = "http://" + u.Host
	case "http", "https":
		base = strings.TrimRight(addr, "/")
	default:
		return nil, fmt.Errorf("engine address %q: unix://, tcp:// or http(s)://", addr)
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Docker{hc: hc, base: base + "/v1.41"}, nil
}

// apiError is the engine's error body.
type apiError struct {
	Status  int
	Message string `json:"message"`
}

func (e *apiError) Error() string { return fmt.Sprintf("engine: %d %s", e.Status, e.Message) }

func (d *Docker) do(ctx context.Context, method, p string, q url.Values, body io.Reader, contentType string, out any) error {
	u := d.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotModified {
		e := &apiError{Status: res.StatusCode}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if json.Unmarshal(b, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		if res.StatusCode == http.StatusNotFound {
			if strings.Contains(strings.ToLower(e.Message), "image") {
				return fmt.Errorf("%w: %s", ErrNoSuchImage, e.Message)
			}
			return fmt.Errorf("%w: %s", ErrNotFound, e.Message)
		}
		return e
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (d *Docker) doJSON(ctx context.Context, method, p string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	return d.do(ctx, method, p, q, body, "application/json", out)
}

func (d *Docker) Ping(ctx context.Context) error {
	return d.do(ctx, http.MethodGet, "/_ping", nil, nil, "", nil)
}

type networkJSON struct {
	ID       string `json:"Id"`
	Name     string
	Internal bool
	IPAM     struct {
		Config []struct {
			Subnet  string
			Gateway string
		}
	}
}

func (n networkJSON) view() Network {
	out := Network{ID: n.ID, Name: n.Name, Internal: n.Internal}
	if len(n.IPAM.Config) > 0 {
		out.Subnet, out.Gateway = n.IPAM.Config[0].Subnet, n.IPAM.Config[0].Gateway
	}
	return out
}

func (d *Docker) EnsureNetwork(ctx context.Context, name string, internal bool, labels map[string]string) (Network, error) {
	var n networkJSON
	err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, &n)
	if err == nil {
		if n.Internal != internal {
			return Network{}, fmt.Errorf("network %s exists with internal=%v; remove it or name another", name, n.Internal)
		}
		return n.view(), nil
	}
	if !isNotFound(err) {
		return Network{}, err
	}
	in := map[string]any{"Name": name, "Driver": "bridge", "Internal": internal, "CheckDuplicate": true, "Labels": labels}
	var made struct {
		ID string `json:"Id"`
	}
	if err := d.doJSON(ctx, http.MethodPost, "/networks/create", nil, in, &made); err != nil {
		return Network{}, err
	}
	if err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(made.ID), nil, nil, &n); err != nil {
		return Network{}, err
	}
	return n.view(), nil
}

func (d *Docker) ConnectNetwork(ctx context.Context, network, container string, aliases []string) error {
	in := map[string]any{"Container": container, "EndpointConfig": map[string]any{"Aliases": aliases}}
	err := d.doJSON(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, in, nil)
	var ae *apiError
	if err != nil && errors.As(err, &ae) && (ae.Status == http.StatusForbidden || strings.Contains(ae.Message, "already exists")) {
		return nil // already connected
	}
	return err
}

func (d *Docker) Pull(ctx context.Context, image string) error {
	name, tag := splitImage(image)
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	// The body is a stream of progress messages; an error can come as one of them.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+"/images/create?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return &apiError{Status: res.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	dec := json.NewDecoder(res.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("pulling %s: %s", image, m.Error)
		}
	}
}

// splitImage cuts "repo/name:tag" into name and tag; a digest stays with the name.
func splitImage(image string) (string, string) {
	if strings.Contains(image, "@") {
		return image, ""
	}
	slash := strings.LastIndex(image, "/")
	if i := strings.LastIndex(image, ":"); i > slash {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

func (d *Docker) Create(ctx context.Context, s ContainerSpec) (string, error) {
	host := map[string]any{
		"CapDrop":       []string{"ALL"},
		"SecurityOpt":   []string{"no-new-privileges:true"},
		"Init":          true,
		"RestartPolicy": map[string]any{"Name": "no"},
		"NetworkMode":   s.Network,
		// The writable layer is the session's disk; nothing else of it is kept.
		"AutoRemove": false,
	}
	if s.Runtime != "" {
		host["Runtime"] = s.Runtime
	}
	if s.CPUMillis > 0 {
		host["NanoCpus"] = int64(s.CPUMillis) * 1_000_000
	}
	if s.MemoryBytes > 0 {
		host["Memory"] = s.MemoryBytes
		host["MemorySwap"] = s.MemoryBytes // no swap on top
	}
	if s.Pids > 0 {
		host["PidsLimit"] = s.Pids
	}
	if s.DiskBytes > 0 {
		host["StorageOpt"] = map[string]string{"size": strconv.FormatInt(s.DiskBytes, 10)}
	}
	var mounts []map[string]any
	for _, m := range s.Mounts {
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": m.Source, "Target": m.Target, "ReadOnly": m.ReadOnly})
	}
	if len(mounts) > 0 {
		host["Mounts"] = mounts
	}
	in := map[string]any{
		"Image":      s.Image,
		"Env":        s.Env,
		"Labels":     s.Labels,
		"User":       s.User,
		"HostConfig": host,
	}
	if len(s.Cmd) > 0 {
		in["Cmd"] = s.Cmd
	}
	if s.Network != "" {
		in["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{s.Network: map[string]any{}}}
	}
	q := url.Values{}
	if s.Name != "" {
		q.Set("name", s.Name)
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := d.doJSON(ctx, http.MethodPost, "/containers/create", q, in, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (d *Docker) PutFiles(ctx context.Context, id string, files []File) error {
	byDir := map[string][]File{}
	for _, f := range files {
		byDir[path.Dir(f.Path)] = append(byDir[path.Dir(f.Path)], f)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, f := range byDir[dir] {
			hdr := &tar.Header{Name: path.Base(f.Path), Mode: f.Mode, Size: int64(len(f.Data)), Uid: f.UID, Gid: f.GID, ModTime: time.Now(), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write(f.Data); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := d.do(ctx, http.MethodPut, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {dir}}, &buf, "application/x-tar", nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *Docker) Start(ctx context.Context, id string) error {
	return d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, "", nil)
}

func (d *Docker) Stop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout / time.Second))}}
	err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil, "", nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (d *Docker) Remove(ctx context.Context, id string) error {
	err := d.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}, "v": {"1"}}, nil, "", nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

type containerJSON struct {
	ID              string `json:"Id"`
	Names           []string
	Labels          map[string]string
	State           string
	Created         int64
	SizeRw          *int64
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string
		}
	}
}

func (d *Docker) List(ctx context.Context, labels map[string]string, sizes bool) ([]Container, error) {
	var filter []string
	for k, v := range labels {
		filter = append(filter, k+"="+v)
	}
	sort.Strings(filter)
	f, _ := json.Marshal(map[string][]string{"label": filter})
	q := url.Values{"all": {"1"}, "filters": {string(f)}}
	if sizes {
		q.Set("size", "1")
	}
	var rows []containerJSON
	if err := d.doJSON(ctx, http.MethodGet, "/containers/json", q, nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(rows))
	for _, r := range rows {
		c := Container{ID: r.ID, Labels: r.Labels, Running: r.State == "running", Created: time.Unix(r.Created, 0), IPs: map[string]string{}, SizeRw: -1}
		if len(r.Names) > 0 {
			c.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		if sizes && r.SizeRw != nil {
			c.SizeRw = *r.SizeRw
		}
		for name, n := range r.NetworkSettings.Networks {
			if n.IPAddress != "" {
				c.IPs[name] = n.IPAddress
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func isNotFound(err error) bool {
	return err != nil && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoSuchImage))
}
