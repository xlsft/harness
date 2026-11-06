package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A process in the sandbox sees a plain EPERM ("Operation not permitted"): nothing says who refused. So a failed command's
// output is read for the paths and the network errors it names, and each is checked against the policy, which knows the
// answer. A "Permission denied" on a path the policy allows is the file's own mode, and no business of the sandbox.

// BlockedHosts are the hosts the proxy refused since this command started.
func (w *Wrapped) BlockedHosts() []string {
	if w == nil || w.proxy == nil {
		return nil
	}
	return w.proxy.blockedSince(w.since)
}

var (
	refusedRe = regexp.MustCompile(`(?i)operation not permitted|\bEPERM\b|permission denied|\bEACCES\b`)
	// A word that looks like a path, quoted or not.
	pathWordRe = regexp.MustCompile(`(?:^|[\s'"‘“(=:])((?:~|\.{1,2})?/[^\s'"’”:,;)]+|[\w.@+-]+(?:/[\w.@+-]+)+|\.[\w.-]+)`)

	dnsRe = regexp.MustCompile(`(?i)could not resolve|couldn't resolve|name or service not known|nodename nor servname|no such host|getaddrinfo|temporary failure in name resolution|ENOTFOUND|EAI_AGAIN|name resolution failed|lookup [^ ]+ on|lookup [^ ]+: no`)
	// A socket call the kernel refused.
	netRe    = regexp.MustCompile(`(?i)(connect|socket|dial|bind|listen|sendto|fetch)[^\n]*(operation not permitted|EPERM)|(operation not permitted|EPERM)[^\n]*(connect|socket|bind|listen)|EADDRNOTAVAIL|failed to connect to [^ ]+ port`)
	sshRe    = regexp.MustCompile(`(?i)(ssh: |ssh_|could not read from remote repository|host key verification failed|permission denied \(publickey)`)
	dockerRe = regexp.MustCompile(`(?i)docker daemon|docker\.sock|colima|podman\.sock`)
	proxyRe  = regexp.MustCompile(`(?i)harness sandbox: ([^\s]+) is not on the allowed hosts`)
	// git's keychain helper (-50 is its refused call to securityd), and what git says when no helper answered.
	credsRe = regexp.MustCompile(`(?i)could not read (username|password) for|failed to get: -|secitem|seckeychain|keychain`)
	// The xcrun shims complain about their cache on a miss (driver_darwin.go), and carry on.
	noiseRe = regexp.MustCompile(`xcrun_db`)
)

// A Denial says what the sandbox refused a command, as far as that can be told.
type Denial struct {
	// Paths are the file accesses found, each as "path: why".
	Paths []string
	// Hosts are the hosts the proxy refused.
	Hosts []string
	// Network says the kernel refused a direct connection or a name lookup.
	Network bool
	// SSH says it was git or anything else over SSH.
	SSH bool
	// Local says binding or reaching a local port or socket was refused.
	Local bool
	// Credentials says a tool looked for the user's stored credentials: the keychain or a credential helper.
	Credentials bool
	// Unnamed says the output shows a refusal the sandbox may be behind, with nothing that names what.
	Unnamed bool
}

func (d Denial) empty() bool {
	return len(d.Paths)+len(d.Hosts) == 0 && !d.Network && !d.SSH && !d.Local && !d.Credentials && !d.Unnamed
}

// Diagnose reads the output of a command that ran under w. failed says it did not succeed: a command that succeeded
// reports only the hosts it was refused, since it may have tolerated that. cwd resolves relative paths. It returns nil
// when nothing points at the sandbox.
func (w *Wrapped) Diagnose(output, cwd string, failed bool) *Denial {
	if w == nil {
		return nil
	}
	d := Denial{Hosts: w.BlockedHosts()}
	for _, m := range proxyRe.FindAllStringSubmatch(output, -1) {
		if !slices.Contains(d.Hosts, m[1]) {
			d.Hosts = append(d.Hosts, m[1])
		}
	}
	if failed {
		w.readLines(&d, output, cwd)
	}
	if d.empty() {
		return nil
	}
	return &d
}

func (w *Wrapped) readLines(d *Denial, output, cwd string) {
	refused := false
	for _, line := range strings.Split(output, "\n") {
		if len(line) > 2000 {
			line = line[:2000]
		}
		switch {
		case noiseRe.MatchString(line):
			continue
		case credsRe.MatchString(line):
			d.Credentials = true
			continue
		case dockerRe.MatchString(line):
			d.Local = true
			continue
		case sshRe.MatchString(line):
			d.SSH = true
			continue
		case dnsRe.MatchString(line):
			d.Network = true
			continue
		case netRe.MatchString(line):
			if strings.Contains(strings.ToLower(line), "bind") || strings.Contains(strings.ToLower(line), "listen") || strings.Contains(line, "127.0.0.1") || strings.Contains(line, "localhost") {
				d.Local = true
			} else {
				d.Network = true
			}
			continue
		}
		if !refusedRe.MatchString(line) {
			continue
		}
		eperm := strings.Contains(strings.ToLower(line), "not permitted") || strings.Contains(line, "EPERM")
		named := false
		for _, m := range pathWordRe.FindAllStringSubmatch(line, -1) {
			p := m[1]
			switch {
			case strings.HasPrefix(p, "~/") && w.Policy.Home != "":
				p = filepath.Join(w.Policy.Home, p[2:])
			case !filepath.IsAbs(p):
				p = filepath.Join(cwd, p)
			}
			// The sandbox's refusal is EPERM. An EACCES can only be the file's own mode, unless it is on a deny list.
			why := w.Policy.Why(p, eperm)
			if st, err := os.Stat(p); err == nil && eperm && st.Mode()&os.ModeSetuid != 0 {
				why = "it is a setuid program (ps, top, sudo...), which cannot start in a sandbox; pgrep and lsof can stand in for ps"
			}
			if why == "" {
				continue
			}
			if entry := m[1] + ": " + why; !slices.Contains(d.Paths, entry) {
				d.Paths = append(d.Paths, entry)
			}
			named = true
		}
		refused = refused || (eperm && !named)
	}
	d.Unnamed = refused && len(d.Paths) == 0 && !d.Network && !d.Local && !d.SSH
	if d.Unnamed {
		w.readReport(d)
	}
	if len(d.Paths) > 5 {
		d.Paths = d.Paths[:5]
	}
}

// readReport asks the driver what the output did not say.
func (w *Wrapped) readReport(d *Denial) {
	r, ok := w.driver.(Reporter)
	if !ok {
		return
	}
	for _, refusal := range r.Refusals(w.Policy.Tag, w.started) {
		op, target, _ := strings.Cut(refusal, " ")
		switch {
		case strings.HasPrefix(op, "network"):
			if strings.Contains(target, "localhost") || strings.Contains(target, "127.0.0.1") || op != "network-outbound" || strings.HasPrefix(target, "/") {
				d.Local = true
			} else {
				d.Network = true
			}
		case op == "mach-lookup":
			d.Paths = append(d.Paths, "the system service "+target+": sandboxed commands reach only a short list of services")
		case strings.HasPrefix(op, "file-"):
			why := w.Policy.Why(target, !strings.HasPrefix(op, "file-read"))
			if why == "" {
				why = "the sandbox refused " + op
			}
			d.Paths = append(d.Paths, target+": "+why)
		case op == "process-exec" || op == "process-exec*":
			d.Paths = append(d.Paths, target+": it is a setuid program (ps, top, sudo...), which cannot start in a sandbox")
		default:
			d.Paths = append(d.Paths, strings.TrimSpace(op+" "+target)+": the sandbox refused it")
		}
	}
	d.Unnamed = len(d.Paths) == 0 && !d.Network && !d.Local
}

// Note is the text for the model. It says what happened and what will not help, so that it does not loop.
func (d *Denial) Note(outsideParam string) string {
	if d == nil {
		return ""
	}
	what := slices.Clone(d.Paths)
	if d.Credentials {
		what = append(what, "the user's stored credentials: the keychain and credential helpers are closed to sandboxed commands, so what needs a login (git push over HTTPS, say) cannot get one here")
	}
	if len(d.Hosts) > 0 {
		what = append(what, "the network request to "+strings.Join(d.Hosts, ", ")+": not on the allowed hosts")
	}
	if d.SSH {
		what = append(what, "SSH: the sandbox keeps ~/.ssh from commands and allows no direct connection, so git over SSH cannot work in it (an HTTPS remote on an allowed host can)")
	}
	if d.Network {
		what = append(what, "a direct network connection or name lookup: commands reach the network only through the sandbox's HTTP proxy, to the allowed hosts")
	}
	if d.Local {
		what = append(what, "a local port or socket: commands cannot listen or reach local services unless the user turns on Allow localhost in Settings > Sandbox")
	}
	var b strings.Builder
	b.WriteString("[sandbox] ")
	if len(what) > 0 {
		b.WriteString("This command ran in the OS sandbox, which refused: " + strings.Join(what, "; ") + ".")
	} else {
		b.WriteString("This command ran in the OS sandbox and something was refused (Operation not permitted). The output does not say what; it may be a system service, or a file the tool did not name.")
	}
	b.WriteString(" The same command will fail the same way, so do not retry it as it is. Change the approach")
	if outsideParam != "" {
		b.WriteString(", or call it again with " + outsideParam + ": true, which asks the user to run it outside the sandbox,")
	}
	b.WriteString(" or tell the user what is needed: they can add a write path or an allowed host in Settings > Sandbox.")
	return b.String()
}

// Summary is the short form for the frontends: what was refused, with no advice.
func (d *Denial) Summary() string {
	if d == nil {
		return ""
	}
	var parts []string
	for _, p := range d.Paths {
		path, _, _ := strings.Cut(p, ": ")
		parts = append(parts, path)
	}
	parts = append(parts, d.Hosts...)
	if d.Credentials {
		parts = append(parts, "stored credentials")
	}
	switch {
	case d.SSH:
		parts = append(parts, "SSH")
	case d.Network:
		parts = append(parts, "network")
	}
	if d.Local {
		parts = append(parts, "local port or socket")
	}
	if len(parts) == 0 {
		return "Blocked by the sandbox"
	}
	return "Blocked by the sandbox: " + strings.Join(parts, ", ")
}
