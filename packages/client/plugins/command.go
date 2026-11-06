package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Caller says who runs a command.
type Caller struct {
	// Remote: from another device, through the relay. It may look, not change: this device's plugins are this device's call.
	Remote bool
	// Place is the folder statuses are told for.
	Place Place
	// NoApprove refuses approve here (a shell without a terminal, which is what the model's commands have).
	NoApprove string
	// Prefix starts the commands named in messages: "/plugin" or "harness-client plugin".
	Prefix string
}

// Usage lists the commands.
func Usage(prefix string) string {
	lines := []string{
		"[list]                      what is installed, and what each can do here",
		"info <id>                   what a plugin holds",
		"install <source>            name@marketplace, a folder or a repository; runs nothing yet",
		"approve <id>                let what is installed now run on this device",
		"revoke <id>                 take the approval back",
		"enable|disable <id>",
		"update [<id>]               every plugin without an id",
		"pin|unpin <id>              hold a plugin on its version",
		"remove <id> [--keep-data]",
		"marketplace add <source>    owner/repo, a git URL, a URL of a marketplace.json, or a folder",
		"marketplace update [<name>]",
		"marketplace remove <name>   and the plugins installed from it",
		"marketplace list",
		"new <name> [<folder>]       write a plugin's starting files",
		"validate <folder>           read a plugin or marketplace and say what's wrong",
	}
	var b strings.Builder
	b.WriteString("Usage:\n")
	for _, l := range lines {
		b.WriteString("  " + prefix + " " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RunCommand runs one command line (without the command's own name) and returns what to show.
func RunCommand(ctx context.Context, m *Manager, args []string, c Caller) (string, error) {
	if c.Prefix == "" {
		c.Prefix = "/plugin"
	}
	if len(args) == 0 {
		return listText(m, c)
	}
	verb, rest := args[0], args[1:]
	mutating := verb != "list" && verb != "info" && verb != "validate" && verb != "help" && !(verb == "marketplace" && (len(rest) == 0 || rest[0] == "list"))
	if mutating && c.Remote {
		return "", errors.New("plugins are this device's own: change them at the device")
	}
	need := func() (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("say which plugin: %s %s <id>", c.Prefix, verb)
		}
		return resolveID(m, rest[0]), nil
	}
	switch verb {
	case "help", "--help", "-h":
		return Usage(c.Prefix), nil
	case "list":
		return listText(m, c)
	case "info":
		id, err := need()
		if err != nil {
			return "", err
		}
		in, err := m.Find(id, c.Place)
		if err != nil {
			return "", err
		}
		return infoText(in), nil
	case "install":
		if len(rest) != 1 {
			return "", fmt.Errorf("say what to install: %s install <name@marketplace, a folder or a repository>", c.Prefix)
		}
		rec, err := m.Install(ctx, rest[0])
		if err != nil {
			return "", err
		}
		in, err := m.Find(rec.ID, c.Place)
		if err != nil {
			return "", err
		}
		return "Installed " + rec.ID + ". Nothing of it runs yet.\n" + infoText(in) + "\nAfter reading this: " + c.Prefix + " approve " + rec.ID, nil
	case "approve":
		if c.NoApprove != "" {
			return "", errors.New(c.NoApprove)
		}
		id, err := need()
		if err != nil {
			return "", err
		}
		in, err := m.Find(id, c.Place)
		if err != nil {
			return "", err
		}
		if err := m.Approve(id, in.Hash); err != nil {
			return "", err
		}
		return "Approved " + id + " on this device. Hooks and MCP servers start with the next session; skills, commands and output styles apply now.\n" + infoText(in), nil
	case "revoke":
		id, err := need()
		if err != nil {
			return "", err
		}
		if err := m.Revoke(id); err != nil {
			return "", err
		}
		return "Took back the approval of " + id + ". What a running session has started stays until it ends.", nil
	case "enable", "disable":
		id, err := need()
		if err != nil {
			return "", err
		}
		if err := m.SetEnabled(id, verb == "enable"); err != nil {
			return "", err
		}
		return strings.ToUpper(verb[:1]) + verb[1:] + "d " + id + ". Hooks and MCP servers follow with the next session.", nil
	case "pin", "unpin":
		id, err := need()
		if err != nil {
			return "", err
		}
		if err := m.SetPinned(id, verb == "pin"); err != nil {
			return "", err
		}
		return strings.ToUpper(verb[:1]) + verb[1:] + "ned " + id + ".", nil
	case "update":
		return updateText(ctx, m, rest, c)
	case "remove", "uninstall":
		keep := false
		var ids []string
		for _, a := range rest {
			if a == "--keep-data" {
				keep = true
			} else {
				ids = append(ids, a)
			}
		}
		if len(ids) != 1 {
			return "", fmt.Errorf("say which plugin: %s remove <id> [--keep-data]", c.Prefix)
		}
		id := resolveID(m, ids[0])
		if err := m.Remove(id, keep); err != nil {
			return "", err
		}
		return "Removed " + id + ".", nil
	case "marketplace":
		return marketplaceText(ctx, m, rest, c)
	case "new":
		if len(rest) < 1 || len(rest) > 2 {
			return "", fmt.Errorf("say its name: %s new <name> [<folder>]", c.Prefix)
		}
		dir := rest[0]
		if len(rest) == 2 {
			dir = rest[1]
		}
		if !filepath.IsAbs(dir) && c.Place.Cwd != "" {
			dir = filepath.Join(c.Place.Cwd, dir)
		}
		if err := Scaffold(dir, rest[0]); err != nil {
			return "", err
		}
		return "Wrote a plugin in " + dir + ". Try it: " + c.Prefix + " install " + dir, nil
	case "validate":
		if len(rest) != 1 {
			return "", fmt.Errorf("say what to read: %s validate <folder>", c.Prefix)
		}
		dir := rest[0]
		if !filepath.IsAbs(dir) && c.Place.Cwd != "" {
			dir = filepath.Join(c.Place.Cwd, dir)
		}
		return Validate(dir)
	}
	return "", fmt.Errorf("unknown command %q\n%s", verb, Usage(c.Prefix))
}

// resolveID lets a plugin be named without its marketplace when only one has that name.
func resolveID(m *Manager, text string) string {
	if strings.Contains(text, "@") {
		return text
	}
	st, err := m.snapshot()
	if err != nil {
		return text
	}
	found := ""
	for id, r := range st.Plugins {
		if r.Name == text {
			if found != "" {
				return text
			}
			found = id
		}
	}
	if found == "" {
		return text
	}
	return found
}

func updateText(ctx context.Context, m *Manager, rest []string, c Caller) (string, error) {
	var ids []string
	if len(rest) > 0 {
		ids = []string{resolveID(m, rest[0])}
	} else {
		st, err := m.snapshot()
		if err != nil {
			return "", err
		}
		for id := range st.Plugins {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "No plugins are installed.", nil
	}
	var lines []string
	for _, id := range sorted(ids) {
		res, err := m.Update(ctx, id)
		switch {
		case err != nil:
			lines = append(lines, id+": "+err.Error())
		case res.Changed:
			lines = append(lines, fmt.Sprintf("%s is now %s. It needs approving again: %s approve %s", id, res.Record.Version, c.Prefix, id))
		default:
			lines = append(lines, res.Note)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func marketplaceText(ctx context.Context, m *Manager, rest []string, c Caller) (string, error) {
	verb := "list"
	if len(rest) > 0 {
		verb, rest = rest[0], rest[1:]
	}
	switch verb {
	case "list":
		list, err := m.Marketplaces()
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "No marketplaces are registered. Add one: " + c.Prefix + " marketplace add <owner/repo, a git URL, a URL of a marketplace.json, or a folder>", nil
		}
		var b strings.Builder
		for _, mk := range list {
			fmt.Fprintf(&b, "%s (%s)", mk.Name, mk.Source)
			if mk.Refusal != "" {
				b.WriteString(": " + mk.Refusal)
			}
			if mk.Problem != "" {
				b.WriteString(": " + mk.Problem)
			}
			b.WriteString("\n")
			for _, e := range mk.Plugins {
				line := "  " + e.Name + "@" + mk.Name
				if e.Description != "" {
					line += ": " + e.Description
				}
				b.WriteString(line + "\n")
			}
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "add":
		if len(rest) != 1 {
			return "", fmt.Errorf("say where it is: %s marketplace add <source>", c.Prefix)
		}
		s, err := ParseSource(rest[0])
		if err != nil {
			return "", err
		}
		rec, err := m.AddMarketplace(ctx, s)
		if err != nil {
			return "", err
		}
		return "Added the marketplace " + rec.Name + ". Nothing was installed.", nil
	case "update":
		var names []string
		if len(rest) > 0 {
			names = rest
		} else {
			list, err := m.Marketplaces()
			if err != nil {
				return "", err
			}
			for _, mk := range list {
				names = append(names, mk.Name)
			}
		}
		var lines []string
		for _, n := range names {
			if err := m.UpdateMarketplace(ctx, n); err != nil {
				lines = append(lines, n+": "+err.Error())
			} else {
				lines = append(lines, "Updated "+n+".")
			}
		}
		return strings.Join(lines, "\n"), nil
	case "remove":
		if len(rest) != 1 {
			return "", fmt.Errorf("say which: %s marketplace remove <name>", c.Prefix)
		}
		if err := m.RemoveMarketplace(rest[0]); err != nil {
			return "", err
		}
		return "Removed the marketplace " + rest[0] + " and the plugins installed from it.", nil
	}
	return "", fmt.Errorf("unknown command %q\n%s", verb, Usage(c.Prefix))
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

var statusText = map[Status]string{
	StatusActive: "runs here", StatusNeedsApproval: "needs approval", StatusDisabled: "off", StatusUntrustedFolder: "folder not trusted",
	StatusBlockedByPolicy: "blocked by policy", StatusChangedOnDisk: "changed on disk", StatusBroken: "can't be read", StatusNotInstalled: "not installed",
}

func listText(m *Manager, c Caller) (string, error) {
	list, err := m.List(c.Place)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "No plugins are installed. See " + c.Prefix + " help.", nil
	}
	var b strings.Builder
	for _, in := range list {
		fmt.Fprintf(&b, "%s", in.ID)
		if in.Version != "" {
			b.WriteString(" " + in.Version)
		}
		b.WriteString(": " + statusText[in.Status])
		if in.Pinned {
			b.WriteString(", pinned")
		}
		if in.Managed {
			b.WriteString(", from your organization")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func infoText(in Info) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", in.ID)
	if in.Version != "" {
		b.WriteString(" " + in.Version)
	}
	b.WriteString(": " + statusText[in.Status] + "\n")
	if in.Description != "" {
		b.WriteString(in.Description + "\n")
	}
	if in.Reason != "" {
		b.WriteString(in.Reason + "\n")
	}
	if in.Commit != "" {
		b.WriteString("Commit: " + in.Commit + "\n")
	}
	if in.Hash != "" {
		b.WriteString("Content: " + in.Hash + "\n")
	}
	list := func(what string, items []string) {
		if len(items) > 0 {
			b.WriteString(what + ": " + strings.Join(items, ", ") + "\n")
		}
	}
	c := in.Contents
	list("Skills", c.Skills)
	list("Commands", c.Commands)
	list("Agents", c.Agents)
	list("Output styles", c.OutputStyles)
	list("Tools it grants while a skill runs", c.AllowedTools)
	for _, h := range c.Hooks {
		line := "Hook " + h.Event
		if h.Matcher != "" {
			line += " (" + h.Matcher + ")"
		}
		line += ": " + h.Command
		if h.From != "" {
			line += ", while " + h.From + " runs"
		}
		b.WriteString(line + "\n")
	}
	for _, s := range c.MCPServers {
		b.WriteString("MCP server " + s.Name + " (" + s.Transport + "): " + s.Command + "\n")
	}
	list("Not acted on", c.Skipped)
	list("Problems", in.Problems)
	return strings.TrimRight(b.String(), "\n")
}

// Scaffold writes a plugin's starting files: one skill, in the layout other tools read too.
func Scaffold(dir, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("the name %q may use only letters, digits, \".\", \"_\" and \"-\", and must start with a letter or digit", name)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", ManifestFile)); err == nil {
		return fmt.Errorf("%s already holds a plugin", dir)
	}
	manifest, err := json.MarshalIndent(map[string]any{"name": name, "version": "0.1.0", "description": "What this plugin is for"}, "", "  ")
	if err != nil {
		return err
	}
	files := map[string]string{
		".claude-plugin/" + ManifestFile: string(manifest) + "\n",
		"skills/hello/SKILL.md":          "---\ndescription: Says hello, to show this plugin works\n---\n\nGreet the user, and mention that this text is in ${CLAUDE_PLUGIN_ROOT}/skills/hello/SKILL.md.\n",
	}
	for rel, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Validate reads a plugin or marketplace folder and says what's wrong with it. It copies and runs nothing.
func Validate(dir string) (string, error) {
	if p, err := findManifest(dir, ""); err == nil {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		man, err := parseMarketplace(data)
		if err != nil {
			return "", err
		}
		var problems []string
		seen := map[string]bool{}
		for _, e := range man.Plugins {
			if !ValidName(e.Name) {
				problems = append(problems, fmt.Sprintf("the plugin name %q can't be used", e.Name))
			}
			if seen[e.Name] {
				problems = append(problems, "duplicate plugin name "+e.Name)
			}
			seen[e.Name] = true
			rel, _, err := entrySource(e, man)
			if err != nil {
				problems = append(problems, e.Name+": "+err.Error())
				continue
			}
			if rel != "" {
				pl, err := Load(filepath.Join(dir, filepath.FromSlash(rel)), e.Name, man.Name, "", overlayOf(&e))
				if err != nil {
					problems = append(problems, e.Name+": "+err.Error())
					continue
				}
				for _, pr := range pl.Problems {
					problems = append(problems, e.Name+": "+pr)
				}
			}
		}
		if len(problems) > 0 {
			return "", errors.New(strings.Join(problems, "\n"))
		}
		return fmt.Sprintf("The marketplace %s is valid. Plugins: %d.", man.Name, len(man.Plugins)), nil
	}
	p, err := Load(dir, filepath.Base(dir), LocalMarketplace, "", nil)
	if err != nil {
		return "", err
	}
	if p.Contents.Empty() {
		return "", errors.New("the folder holds no plugin: no manifest components and none of the standard folders")
	}
	if len(p.Problems) > 0 {
		return "", errors.New(strings.Join(p.Problems, "\n"))
	}
	out := "The plugin " + p.Name + " is valid."
	if len(p.Contents.Skipped) > 0 {
		out += "\nNot acted on by this client: " + strings.Join(p.Contents.Skipped, ", ")
	}
	return out, nil
}
