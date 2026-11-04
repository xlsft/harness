package preview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xlsft/harness/packages/client/permissions"
)

// LaunchFile is the project's list of dev servers, relative to the session's working directory.
var LaunchFile = filepath.Join(".harness", "launch.json")

// launchFormat explains the file; PreviewStart returns it when the file is missing or wrong, so the model can
// write it.
const launchFormat = `Create it like this:
{
  "servers": [
    {"name": "web", "command": "pnpm dev", "args": ["--port", "5173"], "port": 5173}
  ]
}
name: what PreviewStart calls it (letters, digits, ".", "_", "-"). command: a shell command line, run from the working directory. args: optional, appended to it with shell quoting. port: the TCP port the server listens on; the server is ready when it answers. url: optional, the page to open (default http://localhost:<port>); it must be on this machine.`

// LaunchConfig is one entry of the launch file.
type LaunchConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Port    int      `json:"port"`
	URL     string   `json:"url,omitempty"`
}

// CommandLine is what runs: the command, then the arguments, quoted. The permission check sees this same string.
func (c LaunchConfig) CommandLine() string {
	parts := append([]string{strings.TrimSpace(c.Command)}, quoteArgs(c.Args)...)
	return strings.Join(parts, " ")
}

// PageURL is where the browser opens the server.
func (c LaunchConfig) PageURL() string {
	if c.URL != "" {
		return c.URL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	plainArg    = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
)

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if plainArg.MatchString(a) {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return out
}

// ReadLaunch reads the launch file of a working directory.
func ReadLaunch(cwd string) ([]LaunchConfig, error) {
	path := filepath.Join(cwd, LaunchFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist. %s", path, launchFormat)
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers []LaunchConfig `json:"servers"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%s is not valid: %v. %s", path, err, launchFormat)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s is not valid: trailing content after the JSON object. %s", path, launchFormat)
	}
	seen := map[string]bool{}
	for _, c := range file.Servers {
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("%s is not valid: %v. %s", path, err, launchFormat)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("%s is not valid: the name %q appears twice", path, c.Name)
		}
		seen[c.Name] = true
	}
	return file.Servers, nil
}

func (c LaunchConfig) validate() error {
	switch {
	case !namePattern.MatchString(c.Name):
		return fmt.Errorf("server name %q must be 1 to 64 letters, digits, dots, underscores or hyphens", c.Name)
	case strings.TrimSpace(c.Command) == "":
		return fmt.Errorf("server %q has no command", c.Name)
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("server %q needs a port from 1 to 65535", c.Name)
	}
	// The page opens without a site check of its own (the check of this call is the command's), so it stays on
	// this machine, where the browser tools need no approval anyway.
	if c.URL != "" && !permissions.IsLocalWebPage(c.URL) {
		return fmt.Errorf("server %q has url %q, which is not an http or https page on this machine", c.Name, c.URL)
	}
	return nil
}

// FindLaunch returns the named entry of the launch file.
func FindLaunch(cwd, name string) (LaunchConfig, error) {
	all, err := ReadLaunch(cwd)
	if err != nil {
		return LaunchConfig{}, err
	}
	names := make([]string, 0, len(all))
	for _, c := range all {
		if c.Name == name {
			return c, nil
		}
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return LaunchConfig{}, fmt.Errorf("%s defines no servers. %s", filepath.Join(cwd, LaunchFile), launchFormat)
	}
	return LaunchConfig{}, fmt.Errorf("%s has no server named %q; it defines: %s", filepath.Join(cwd, LaunchFile), name, strings.Join(names, ", "))
}
