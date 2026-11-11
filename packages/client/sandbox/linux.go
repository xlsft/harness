package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// What the Linux driver decides, without touching Linux: which backend, what that leaves open, and what the helper is told.
// driver_linux.go and stage_linux.go do the rest. docs/design/platforms.md has the reasoning.

// The backends.
const (
	backendBubblewrap = "bubblewrap"
	backendLandlock   = "landlock"
)

// stageArg is the first argument with which a binary that embeds this package runs as the sandbox's helper.
const stageArg = "__harness-sandbox-stage"

// What the helper does.
const (
	stageExec        = "exec"
	stageForward     = "forward"
	stageProbeFilter = "probe-filter"
	stageProbeFiles  = "probe-files"
)

// stageSpec is what the helper is told. It travels on the command line, so it holds no secrets, only paths.
type stageSpec struct {
	Op      string
	Backend string
	// Policy is the part the helper needs. ProxyPort, Localhost and Server are in it too.
	Write, WriteDeny, WriteAllowAfterDeny, ReadDeny []string
	Free                                            string
	ProxyPort                                       int
	Localhost, Server                               bool
	// PrivateNetwork: the command is in a network namespace of its own, reaching the proxy through ProxySocket.
	PrivateNetwork bool
	ProxySocket    string
}

func newStageSpec(p Policy, backend string) stageSpec {
	s := stageSpec{Op: stageExec, Backend: backend, Write: p.Write, WriteDeny: p.WriteDeny, WriteAllowAfterDeny: p.WriteAllowAfterDeny, ReadDeny: p.ReadDeny, Free: p.Free,
		ProxyPort: p.ProxyPort, Localhost: p.Localhost, Server: p.Server, PrivateNetwork: p.PrivateLoopback}
	if p.ProxySocket != "" {
		s.ProxySocket = insideProxySocket
	}
	return s
}

// policy gives planFiles what it reads.
func (s stageSpec) policy() Policy {
	return Policy{Write: s.Write, WriteDeny: s.WriteDeny, WriteAllowAfterDeny: s.WriteAllowAfterDeny, ReadDeny: s.ReadDeny, Free: s.Free}
}

func (s stageSpec) encode() string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err) // Strings, numbers and bools.
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeStageSpec(text string) (stageSpec, error) {
	var s stageSpec
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// linuxProbe is what the machine turned out to allow. An empty error means it worked.
type linuxProbe struct {
	// Arch is the machine's, for the filter.
	Arch string
	// LandlockABI is 0 when LandlockErr says why there is none.
	LandlockABI int
	LandlockErr string
	// FilterErr says the system call filter cannot be installed.
	FilterErr string
	// BwrapPath is empty when BwrapErr says why it cannot be used.
	BwrapPath string
	BwrapErr  string
}

// linuxChoice is what the driver does with that.
type linuxChoice struct {
	// Backend is empty when nothing can confine a command here.
	Backend string
	// Reason says why, or what the backend leaves open.
	Reason string
}

func chooseLinuxBackend(pr linuxProbe) linuxChoice {
	if _, ok := seccompArchs[pr.Arch]; !ok {
		return linuxChoice{Reason: "the Linux command sandbox has no system call table for " + pr.Arch + " (amd64 and arm64 only)"}
	}
	if pr.FilterErr != "" {
		return linuxChoice{Reason: "this kernel or container does not allow a system call filter: " + pr.FilterErr}
	}
	switch {
	case pr.BwrapPath != "":
		return linuxChoice{Backend: backendBubblewrap, Reason: bubblewrapLimits(pr.LandlockABI)}
	case pr.LandlockABI > 0:
		return linuxChoice{Backend: backendLandlock, Reason: landlockLimits(pr.LandlockABI)}
	}
	return linuxChoice{Reason: "neither bubblewrap nor Landlock works here. bubblewrap: " + pr.BwrapErr + ". Landlock: " + pr.LandlockErr}
}

func bubblewrapLimits(abi int) string {
	s := "Running with bubblewrap. A command has a loopback interface of its own, so it cannot reach a server on this machine or one started by another command"
	if abi < 4 {
		s += "; preview servers share this machine's network without limits, since this kernel has no Landlock TCP rules"
	}
	return s + "."
}

func landlockLimits(abi int) string {
	s := "Running with Landlock, without bubblewrap (install it, or give the container user namespaces, for more). Hooks and settings files in a working folder are checked after each command instead of refused; a command's own unix sockets do not work"
	if abi < 4 {
		return s + "; this kernel has no Landlock TCP rules (6.7 or later), so commands have no network and no localhost at all."
	}
	return s + "; the network is limited by port, not by address, so a command can still reach any host on a port from 1024 up."
}

// prepareLinuxPolicy adapts a policy to a backend: what Seatbelt says by pattern about paths that may not exist, a mount or a
// rule needs to find.
func prepareLinuxPolicy(p *Policy, backend string, abi int, proxySocket func() (string, error)) error {
	p.GuardNames = true
	p.DenyHeld = backend == backendBubblewrap
	p.FilesByPermission = backend == backendLandlock
	p.PortRules = abi >= 4
	if backend == backendBubblewrap && !p.Server {
		p.PrivateLoopback = true
		sock, err := proxySocket()
		if err != nil {
			return fmt.Errorf("no socket for the network proxy: %w", err)
		}
		p.ProxySocket = sock
	}
	p.Write = linuxRoots(p.Home, p.Write)
	p.Mkdir = nil
	return nil
}

// alwaysMade are the toolchain folders under home made for a first use, since nothing can make them afterwards: the tools a
// machine that runs an agent is likely to run. Any other is made when the tool has its folder already, which leaves no
// .cargo or .volta in a home that has none.
var alwaysMade = []string{".cache", ".npm", "go"}

// linuxRoots drops macOS's own locations and makes the toolchain roots that do not exist yet: a mount or a rule needs a path.
func linuxRoots(home string, roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if home == "" || !within(r, home) || r == home {
			out = append(out, r)
			continue
		}
		rel := strings.TrimPrefix(r, strings.TrimSuffix(home, "/")+"/")
		first, _, _ := strings.Cut(rel, "/")
		if first == "Library" {
			continue
		}
		if _, err := os.Stat(r); err != nil {
			_, topErr := os.Stat(filepath.Join(home, first))
			made := false
			for _, a := range alwaysMade {
				made = made || a == first
			}
			if topErr == nil || made {
				_ = os.MkdirAll(r, 0o755)
			}
		}
		out = append(out, r)
	}
	return out
}
