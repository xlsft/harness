//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// bwrapPaths are where a package puts it. The program is an absolute path, never looked up on the command's PATH.
var bwrapPaths = []string{"/usr/bin/bwrap", "/bin/bwrap", "/usr/local/bin/bwrap", "/run/current-system/sw/bin/bwrap"}

// probeTimeout bounds each trial: a machine that takes longer is not one a command should wait on.
const probeTimeout = 10 * time.Second

// linuxDriver confines commands with bubblewrap or with Landlock, whichever the machine allows (linux.go).
type linuxDriver struct {
	// backend, when set, is the only one tried: for tests.
	backend string
}

// NewDriver returns the driver of this platform.
func NewDriver() Driver { return linuxDriver{} }

// probed is once per process: what the kernel allows does not change while it runs.
var probed = sync.OnceValue(probeLinux)

func (d linuxDriver) choice() (linuxChoice, linuxProbe) {
	pr := probed()
	switch {
	case d.backend == backendLandlock:
		pr.BwrapPath, pr.BwrapErr = "", "not tried"
	case d.backend == backendBubblewrap && pr.BwrapPath == "":
		return linuxChoice{Reason: "bubblewrap: " + pr.BwrapErr}, pr
	}
	return chooseLinuxBackend(pr), pr
}

func (d linuxDriver) Info() Info {
	c, _ := d.choice()
	return Info{Platform: "linux", Supported: c.Backend != "", Reason: c.Reason}
}

// Prepare implements Preparer.
func (d linuxDriver) Prepare(p *Policy, proxySocket func() (string, error)) error {
	c, pr := d.choice()
	if c.Backend == "" {
		return errors.New(c.Reason)
	}
	return prepareLinuxPolicy(p, c.Backend, pr.LandlockABI, proxySocket)
}

// Command returns the helper in front of argv, and bwrap in front of that when it is the backend.
func (d linuxDriver) Command(p Policy, argv []string) ([]string, error) {
	c, pr := d.choice()
	if c.Backend == "" {
		return nil, errors.New(c.Reason)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("the sandbox's helper is this program, which cannot find itself: %w", err)
	}
	stage := append([]string{self, stageArg, newStageSpec(p, c.Backend).encode()}, argv...)
	if c.Backend == backendLandlock {
		return stage, nil
	}
	masked, err := maskedFile(p.Free)
	if err != nil {
		return nil, fmt.Errorf("the command sandbox could not make its masking file: %w", err)
	}
	var protected []string
	for _, w := range p.Work {
		protected = append(protected, findProtected(w)...)
		for _, g := range gitDirs(w) {
			protected = append(protected, filepath.Join(g, "hooks"))
		}
	}
	in := bwrapInput{Policy: p, Kind: statKind, Masked: masked, Sockets: findSockets(systemTmp), Protected: protected, Stage: stage}
	return append([]string{pr.BwrapPath}, bubblewrapArgs(in)...), nil
}

// maskedFile is an empty file nobody may read, in the session's scratch directory. A command there could change its mode, and
// would find it empty.
func maskedFile(scratch string) (string, error) {
	path := filepath.Join(scratch, "sandbox-masked")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return path, nil // Already there, as it should be.
		}
		return "", err
	}
	_ = f.Close()
	return path, nil
}

func statKind(path string) pathKind {
	st, err := os.Lstat(path)
	switch {
	case err != nil:
		return pathMissing
	case st.IsDir():
		return pathDir
	case st.Mode().IsRegular():
		return pathFile
	}
	return pathOther
}

func probeLinux() linuxProbe {
	pr := linuxProbe{Arch: runtime.GOARCH}
	pr.LandlockABI, pr.LandlockErr = landlockVersion()
	self, err := os.Executable()
	if err != nil {
		pr.FilterErr = "this program cannot find itself: " + err.Error()
		return pr
	}
	if _, ok := seccompArchs[pr.Arch]; !ok {
		return pr
	}
	if err := runProbe(self, stageSpec{Op: stageProbeFilter, Backend: backendBubblewrap, PrivateNetwork: true}); err != nil {
		pr.FilterErr = err.Error()
		return pr
	}
	if pr.LandlockABI > 0 {
		if err := probeFiles(self); err != nil {
			pr.LandlockABI, pr.LandlockErr = 0, err.Error()
		}
	}
	pr.BwrapPath, pr.BwrapErr = probeBwrap(self)
	return pr
}

// probeFiles runs the helper with Landlock on a folder of its own, which checks that the kernel enforces what it accepted.
func probeFiles(self string) error {
	dir, err := os.MkdirTemp("", "hsb-probe")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	allowed := filepath.Join(dir, "allowed")
	if err := os.Mkdir(allowed, 0o700); err != nil {
		return err
	}
	return runProbe(self, stageSpec{Op: stageProbeFiles, Backend: backendLandlock, Write: []string{allowed}}, allowed, filepath.Join(dir, "refused"))
}

func runProbe(self string, s stageSpec, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, self, append([]string{stageArg, s.encode()}, args...)...).CombinedOutput()
	if err != nil {
		return errors.New(probeMessage(out, err))
	}
	return nil
}

func probeMessage(out []byte, err error) string {
	if s := strings.TrimSpace(string(bytes.ToValidUTF8(out, nil))); s != "" {
		return s
	}
	return err.Error()
}

// probeBwrap finds bwrap and tries the namespaces a command will get, with the helper and its filter inside: an installed
// bwrap says nothing about a container's seccomp profile or AppArmor's rules for user namespaces.
func probeBwrap(self string) (string, string) {
	path := ""
	for _, p := range bwrapPaths {
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return "", "it is not installed (looked in " + strings.Join(bwrapPaths, ", ") + ")"
	}
	args := bubblewrapArgs(bwrapInput{Policy: Policy{PrivateLoopback: true}, Kind: statKind,
		Stage: []string{self, stageArg, stageSpec{Op: stageProbeFilter, Backend: backendBubblewrap, PrivateNetwork: true}.encode()}})
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, args...).CombinedOutput(); err != nil {
		return "", path + " cannot make its namespaces here: " + probeMessage(out, err)
	}
	return path, ""
}
