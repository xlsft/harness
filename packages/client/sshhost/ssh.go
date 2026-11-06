package sshhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client runs the user's OpenSSH.
type Client struct {
	// Dir is HARNESS_HOME/ssh: the keys this client remembers are in known_hosts there.
	Dir string
	// SSH and Keygen are the executables ("" for what is on PATH).
	SSH, Keygen string
	// ExtraArgs come first on every ssh command line. Tests name a config of their own with them, so the developer's
	// ~/.ssh takes no part.
	ExtraArgs []string
	// ConnectTimeout bounds reaching a host (0: 15 seconds).
	ConnectTimeout time.Duration
}

func (c *Client) sshPath() string {
	if c.SSH != "" {
		return c.SSH
	}
	return "ssh"
}

func (c *Client) keygenPath() string {
	if c.Keygen != "" {
		return c.Keygen
	}
	return "ssh-keygen"
}

func (c *Client) knownHosts() string { return filepath.Join(c.Dir, "known_hosts") }

func (c *Client) timeoutSeconds() string {
	d := c.ConnectTimeout
	if d <= 0 {
		d = 15 * time.Second
	}
	return strconv.Itoa(int(d / time.Second))
}

// quoteOpt quotes a value for an ssh -o option: its value is split on spaces, like a config file line.
func quoteOpt(v string) string { return `"` + v + `"` }

// args is the command line up to the destination. StrictHostKeyChecking=yes makes ssh refuse any host whose key it does not
// already know, with no way around it from here; BatchMode means it never asks for anything; no connection sharing, so that
// a forward cannot end up on some other process's master.
func (c *Client) args(h Host, extra ...string) []string {
	out := append([]string(nil), c.ExtraArgs...)
	out = append(out, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile="+quoteOpt("~/.ssh/known_hosts")+" "+quoteOpt(c.knownHosts()),
		"-o", "ConnectTimeout="+c.timeoutSeconds(), "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-T")
	return c.withHost(out, h, extra...)
}

func (c *Client) withHost(out []string, h Host, extra ...string) []string {
	if h.Port != 0 {
		out = append(out, "-p", strconv.Itoa(h.Port))
	}
	if h.IdentityFile != "" {
		out = append(out, "-i", h.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	out = append(out, extra...)
	return append(out, "--", h.Destination)
}

// ErrNoSSH says OpenSSH is missing.
var ErrNoSSH = errors.New("the ssh command was not found: install OpenSSH")

// Run runs a command on the host and returns its stdout. The error holds ssh's or the command's last words.
func (c *Client) Run(ctx context.Context, h Host, stdin io.Reader, command string, extra ...string) ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.sshPath(), append(c.args(h, extra...), command)...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNoSSH
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return stdout.Bytes(), &RunError{Stderr: tail(stderr.String()), Err: err}
	}
	return stdout.Bytes(), nil
}

// RunError is a command that failed on the host, or an ssh that failed to get there.
type RunError struct {
	Stderr string
	Err    error
}

func (e *RunError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Err.Error()
}

func (e *RunError) Unwrap() error { return e.Err }

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1500 {
		s = "…" + s[len(s)-1500:]
	}
	return s
}

// KeyStatus is what is known about a host's key.
type KeyStatus int

// Key statuses.
const (
	KeyVerified KeyStatus = iota + 1
	KeyUnknown
	KeyChanged
	KeyUnreachable
)

// KeyCheck is the outcome of CheckKey.
type KeyCheck struct {
	Status KeyStatus
	// Fingerprints are the host's keys as ssh-keygen shows them, when unknown.
	Fingerprints []string
	// Fingerprint is the token TrustKey takes: every key the host offered, as one string.
	Fingerprint string
	// Err is ssh's words when unreachable.
	Err string
}

// CheckKey asks ssh what it makes of the host's key. Authentication is limited to "none" throughout, so no key or password is
// ever offered to a host that has not been verified.
func (c *Client) CheckKey(ctx context.Context, h Host) (KeyCheck, error) {
	if err := h.Validate(); err != nil {
		return KeyCheck{}, err
	}
	cmd := exec.CommandContext(ctx, c.sshPath(), append(c.args(h, "-o", "PreferredAuthentications=none", "-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no"), "true")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return KeyCheck{}, ErrNoSSH
	}
	if err == nil {
		return KeyCheck{Status: KeyVerified}, nil // A host that lets anyone in; its key checked out all the same.
	}
	if ctx.Err() != nil {
		return KeyCheck{}, ctx.Err()
	}
	said := stderr.String()
	switch {
	case strings.Contains(said, "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(said, "Offending"):
		return KeyCheck{Status: KeyChanged}, nil
	case strings.Contains(said, "Host key verification failed") || strings.Contains(said, "No ") && strings.Contains(said, "host key is known"):
		_, fp, err := c.scan(ctx, h)
		if err != nil {
			return KeyCheck{}, err
		}
		return KeyCheck{Status: KeyUnknown, Fingerprints: strings.Split(fp, "\n"), Fingerprint: fp}, nil
	case strings.Contains(said, "Permission denied"):
		// The handshake got past the key to authentication: the key is good.
		return KeyCheck{Status: KeyVerified}, nil
	}
	return KeyCheck{Status: KeyUnreachable, Err: tail(said)}, nil
}

// fingerprintRE takes "256 SHA256:abc... comment (ED25519)" apart.
var fingerprintRE = regexp.MustCompile(`^\d+\s+(SHA256:\S+)\s.*\((\S+)\)\s*$`)

// scan learns the host's keys by connecting with a throwaway known_hosts, offering no credentials, and has ssh-keygen read them.
// It goes through ssh rather than ssh-keyscan so a ProxyJump in the user's config applies. It returns the known_hosts lines and
// the fingerprints, sorted by the file's order, one per line.
func (c *Client) scan(ctx context.Context, h Host) ([]byte, string, error) {
	dir, err := os.MkdirTemp("", "harness-ssh-scan-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "known_hosts")
	out := append([]string(nil), c.ExtraArgs...)
	out = append(out, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile="+quoteOpt(file), "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HashKnownHosts=no",
		"-o", "ConnectTimeout="+c.timeoutSeconds(), "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "CheckHostIP=no",
		"-o", "PreferredAuthentications=none", "-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-T")
	cmd := exec.CommandContext(ctx, c.sshPath(), append(c.withHost(out, Host{Destination: h.Destination, Port: h.Port}), "true")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // "Permission denied" is the expected end: the key is written by then.
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	lines, err := os.ReadFile(file)
	if err != nil || len(bytes.TrimSpace(lines)) == 0 {
		return nil, "", fmt.Errorf("could not read the host's key: %s", tail(stderr.String()))
	}
	kg := exec.CommandContext(ctx, c.keygenPath(), "-lf", file, "-E", "sha256")
	var kout, kerr bytes.Buffer
	kg.Stdout, kg.Stderr = &kout, &kerr
	if err := kg.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, "", ErrNoSSH
		}
		return nil, "", fmt.Errorf("ssh-keygen: %s", tail(kerr.String()))
	}
	var fps []string
	for _, line := range strings.Split(kout.String(), "\n") {
		if m := fingerprintRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			fps = append(fps, m[1]+" ("+m[2]+")")
		}
	}
	if len(fps) == 0 {
		return nil, "", errors.New("ssh-keygen read no key from the host")
	}
	return lines, strings.Join(fps, "\n"), nil
}

// ErrKeyMoved means the host's key is no longer the one the user was shown.
var ErrKeyMoved = errors.New("the host's key is not the one you were shown; check again")

// TrustKey remembers the host's key, if it still is the one with this fingerprint: scanning again closes the gap between what
// the user read and what gets stored.
func (c *Client) TrustKey(ctx context.Context, h Host, fingerprint string) error {
	if err := h.Validate(); err != nil {
		return err
	}
	lines, fp, err := c.scan(ctx, h)
	if err != nil {
		return err
	}
	if fp != fingerprint {
		return ErrKeyMoved
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(c.knownHosts(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if !bytes.HasSuffix(lines, []byte("\n")) {
		lines = append(lines, '\n')
	}
	_, err = f.Write(lines)
	return err
}

// ForgetKey removes what this client remembers about the host's key. The user's own known_hosts is theirs to edit.
func (c *Client) ForgetKey(ctx context.Context, h Host) error {
	if _, err := os.Stat(c.knownHosts()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	name := hostName(ctx, c, h)
	if h.Port != 0 && h.Port != 22 {
		name = "[" + name + "]:" + strconv.Itoa(h.Port)
	}
	cmd := exec.CommandContext(ctx, c.keygenPath(), "-R", name, "-f", c.knownHosts())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh-keygen: %s", tail(stderr.String()))
	}
	_ = os.Remove(c.knownHosts() + ".old")
	return nil
}

// hostName is the name ssh records for the host: an alias resolved through the config, without its user.
func hostName(ctx context.Context, c *Client, h Host) string {
	out := append([]string(nil), c.ExtraArgs...)
	out = append(out, "-G", "--", h.Destination)
	data, err := exec.CommandContext(ctx, c.sshPath(), out...).Output()
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "hostname "); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	if _, after, ok := strings.Cut(h.Destination, "@"); ok {
		return after
	}
	return h.Destination
}

// shellQuote makes s one word of a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
