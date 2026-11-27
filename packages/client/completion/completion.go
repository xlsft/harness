// Package completion writes shell completion scripts (bash, zsh, fish) for a tree of commands whose flags are pflag flag
// sets. harness-client and harness both describe their commands with it, from the same flag sets they parse, so a new flag
// completes without anyone editing a script.
//
// A flag's values complete from annotations on the flag set: Choices for a fixed list, Dirs for a directory. A flag that
// takes some other value completes file names.
package completion

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/pflag"
)

// Annotation keys (pflag.FlagSet.SetAnnotation).
const (
	choicesKey = "harness_completion_choices"
	dirsKey    = "harness_completion_dirs"
)

// Shells this package writes for.
var Shells = []string{"bash", "zsh", "fish"}

// Choices makes the values of a flag complete from a fixed list.
func Choices(fs *pflag.FlagSet, name string, values ...string) {
	_ = fs.SetAnnotation(name, choicesKey, values)
}

// Dirs makes the value of a flag complete as a directory.
func Dirs(fs *pflag.FlagSet, name string) {
	_ = fs.SetAnnotation(name, dirsKey, []string{"true"})
}

// Command is a command or subcommand.
type Command struct {
	Name  string
	Short string
	// Flags of this command (nil: none). Flags of a parent are not inherited.
	Flags *pflag.FlagSet
	// Args are words the first argument may be (bash zsh fish for `completion`), offered with the subcommands.
	Args        []string
	Subcommands []*Command
}

type flag struct {
	long, short, usage string
	value              bool // takes a value
	choices            []string
	dirs               bool
}

func flagsOf(c *Command) []flag {
	if c.Flags == nil {
		return nil
	}
	var out []flag
	c.Flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" {
			return
		}
		out = append(out, flag{
			long:    f.Name,
			short:   f.Shorthand,
			usage:   firstLine(f.Usage),
			value:   f.NoOptDefVal == "" && f.Value.Type() != "bool",
			choices: f.Annotations[choicesKey],
			dirs:    len(f.Annotations[dirsKey]) > 0,
		})
	})
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 90 {
		s = s[:87] + "..."
	}
	return s
}

// node is a command with its place in the tree: id is "root" or "root_sub_subsub", a shell identifier.
type node struct {
	id  string
	cmd *Command
}

func walk(root *Command) []node {
	var out []node
	var visit func(id string, c *Command)
	visit = func(id string, c *Command) {
		out = append(out, node{id, c})
		for _, s := range c.Subcommands {
			visit(id+"_"+ident(s.Name), s)
		}
	}
	visit("root", root)
	return out
}

func ident(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// Write writes the script for one shell.
func Write(w io.Writer, shell string, root *Command) error {
	switch shell {
	case "bash":
		return Bash(w, root)
	case "zsh":
		return Zsh(w, root)
	case "fish":
		return Fish(w, root)
	}
	return fmt.Errorf("unknown shell %q: one of %s", shell, strings.Join(Shells, ", "))
}

// Usage says how to install the scripts, for a command's help.
func Usage(program string) string {
	return fmt.Sprintf(`Usage: %[1]s completion bash|zsh|fish

Prints a completion script. To load it:
  bash   %[1]s completion bash > ~/.%[1]s-completion.bash
         echo 'source ~/.%[1]s-completion.bash' >> ~/.bash_profile
  zsh    %[1]s completion zsh > "${fpath[1]}/_%[1]s"   (then start a new shell)
         or: echo 'source <(%[1]s completion zsh)' >> ~/.zshrc, after compinit
  fish   %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish

A Homebrew install has them already.
`, program)
}

// shq quotes for a POSIX shell (and zsh, and fish, which accept the same single quotes for these strings).
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- bash ---------------------------------------------------------------------------------------------

// Bash writes a script for bash 3.2 and later (macOS ships 3.2).
func Bash(w io.Writer, root *Command) error {
	fn := "_" + ident(root.Name)
	nodes := walk(root)
	var b strings.Builder
	fmt.Fprintf(&b, "# bash completion for %s (bash 3.2 and later). Save it and source the file from ~/.bash_profile.\n", root.Name)
	fmt.Fprintf(&b, "%s() {\n", fn)
	b.WriteString("  local cur prev cmd=root i w\n")
	b.WriteString("  cur=\"${COMP_WORDS[COMP_CWORD]}\"\n  prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n")
	b.WriteString("  for ((i = 1; i < COMP_CWORD; i++)); do\n    w=\"${COMP_WORDS[i]}\"\n    case \"$cmd:$w\" in\n")
	for _, n := range nodes {
		for _, s := range n.cmd.Subcommands {
			fmt.Fprintf(&b, "      %s) cmd=%s ;;\n", shq(n.id+":"+s.Name), n.id+"_"+ident(s.Name))
		}
		if v := valueFlagPatterns(n, flagsOf(n.cmd)); v != "" {
			fmt.Fprintf(&b, "      %s) ((i++)) ;;\n", v)
		}
	}
	b.WriteString("    esac\n  done\n")
	b.WriteString("  case \"$cmd:$prev\" in\n")
	for _, n := range nodes {
		for _, f := range flagsOf(n.cmd) {
			if !f.value {
				continue
			}
			pat := flagPatterns(n.id, f)
			switch {
			case len(f.choices) > 0:
				fmt.Fprintf(&b, "    %s) COMPREPLY=($(compgen -W %s -- \"$cur\")); return ;;\n", pat, shq(strings.Join(f.choices, " ")))
			case f.dirs:
				fmt.Fprintf(&b, "    %s) COMPREPLY=($(compgen -d -- \"$cur\")); return ;;\n", pat)
			default:
				fmt.Fprintf(&b, "    %s) COMPREPLY=($(compgen -f -- \"$cur\")); return ;;\n", pat)
			}
		}
	}
	b.WriteString("  esac\n  local words=\"\"\n  case \"$cmd\" in\n")
	for _, n := range nodes {
		var words []string
		for _, s := range n.cmd.Subcommands {
			words = append(words, s.Name)
		}
		words = append(words, n.cmd.Args...)
		for _, f := range flagsOf(n.cmd) {
			words = append(words, "--"+f.long)
			if f.short != "" {
				words = append(words, "-"+f.short)
			}
		}
		fmt.Fprintf(&b, "    %s) words=%s ;;\n", n.id, shq(strings.Join(words, " ")))
	}
	b.WriteString("  esac\n  COMPREPLY=($(compgen -W \"$words\" -- \"$cur\"))\n}\n")
	fmt.Fprintf(&b, "complete -o default -F %s %s\n", fn, root.Name)
	_, err := io.WriteString(w, b.String())
	return err
}

// flagPatterns is a case pattern matching the flag's spellings under a command id.
func flagPatterns(id string, f flag) string {
	pats := []string{shq(id + ":--" + f.long)}
	if f.short != "" {
		pats = append(pats, shq(id+":-"+f.short))
	}
	return strings.Join(pats, " | ")
}

func valueFlagPatterns(n node, flags []flag) string {
	var pats []string
	for _, f := range flags {
		if f.value {
			pats = append(pats, flagPatterns(n.id, f))
		}
	}
	return strings.Join(pats, " | ")
}

// --- zsh ----------------------------------------------------------------------------------------------

// Zsh writes a script for zsh's completion system (compinit): a #compdef file, which also works when sourced.
func Zsh(w io.Writer, root *Command) error {
	fn := "_" + ident(root.Name)
	nodes := walk(root)
	var b strings.Builder
	fmt.Fprintf(&b, "#compdef %s\n# zsh completion for %s. Install with: %s completion zsh > \"${fpath[1]}/_%s\"\n", root.Name, root.Name, root.Name, root.Name)
	fmt.Fprintf(&b, "%s() {\n", fn)
	b.WriteString("  local cmd=root i w prev cur\n  cur=\"${words[CURRENT]}\"\n  prev=\"${words[CURRENT-1]}\"\n")
	b.WriteString("  for ((i = 2; i < CURRENT; i++)); do\n    w=\"${words[i]}\"\n    case \"$cmd:$w\" in\n")
	for _, n := range nodes {
		for _, s := range n.cmd.Subcommands {
			fmt.Fprintf(&b, "      %s) cmd=%s ;;\n", shq(n.id+":"+s.Name), n.id+"_"+ident(s.Name))
		}
		if v := valueFlagPatterns(n, flagsOf(n.cmd)); v != "" {
			fmt.Fprintf(&b, "      %s) ((i++)) ;;\n", v)
		}
	}
	b.WriteString("    esac\n  done\n  case \"$cmd:$prev\" in\n")
	for _, n := range nodes {
		for _, f := range flagsOf(n.cmd) {
			if !f.value {
				continue
			}
			pat := flagPatterns(n.id, f)
			switch {
			case len(f.choices) > 0:
				fmt.Fprintf(&b, "    %s) compadd -- %s; return ;;\n", pat, strings.Join(quoteAll(f.choices), " "))
			case f.dirs:
				fmt.Fprintf(&b, "    %s) _files -/; return ;;\n", pat)
			default:
				fmt.Fprintf(&b, "    %s) _files; return ;;\n", pat)
			}
		}
	}
	b.WriteString("  esac\n  local -a subs flags\n  case \"$cmd\" in\n")
	for _, n := range nodes {
		var subs, flags []string
		for _, s := range n.cmd.Subcommands {
			subs = append(subs, shq(zshDescribe(s.Name, s.Short)))
		}
		for _, a := range n.cmd.Args {
			subs = append(subs, shq(zshDescribe(a, "")))
		}
		for _, f := range flagsOf(n.cmd) {
			flags = append(flags, shq(zshDescribe("--"+f.long, f.usage)))
			if f.short != "" {
				flags = append(flags, shq(zshDescribe("-"+f.short, f.usage)))
			}
		}
		fmt.Fprintf(&b, "    %s) subs=(%s); flags=(%s) ;;\n", n.id, strings.Join(subs, " "), strings.Join(flags, " "))
	}
	b.WriteString("  esac\n")
	b.WriteString("  if [[ \"$cur\" == -* ]]; then\n    _describe -t flags flag flags\n  elif (( ${#subs} )); then\n    _describe -t commands command subs\n  else\n    _files\n  fi\n}\n")
	// Autoloaded from fpath, the file runs as the function _<program>: complete now. Sourced, register for later.
	fmt.Fprintf(&b, "if [[ \"$funcstack[1]\" == %s ]]; then\n  %s \"$@\"\nelse\n  compdef %s %s\nfi\n", shq("_"+root.Name), fn, fn, root.Name)
	_, err := io.WriteString(w, b.String())
	return err
}

func zshDescribe(name, desc string) string {
	name = strings.ReplaceAll(name, ":", `\:`)
	if desc == "" {
		return name
	}
	return name + ":" + desc
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shq(s)
	}
	return out
}

// --- fish ---------------------------------------------------------------------------------------------

// Fish writes a script for fish's completions directory.
func Fish(w io.Writer, root *Command) error {
	fn := "__" + ident(root.Name) + "_cmd"
	nodes := walk(root)
	var b strings.Builder
	fmt.Fprintf(&b, "# fish completion for %s. Install with: %s completion fish > ~/.config/fish/completions/%s.fish\n", root.Name, root.Name, root.Name)
	fmt.Fprintf(&b, "function %s\n  set -l cmd root\n  set -l skip 0\n  for w in (commandline -opc)[2..-1]\n", fn)
	b.WriteString("    if test $skip = 1\n      set skip 0\n      continue\n    end\n    switch \"$cmd:$w\"\n")
	for _, n := range nodes {
		for _, s := range n.cmd.Subcommands {
			fmt.Fprintf(&b, "      case %s\n        set cmd %s\n", shq(n.id+":"+s.Name), n.id+"_"+ident(s.Name))
		}
		var pats []string
		for _, f := range flagsOf(n.cmd) {
			if f.value {
				pats = append(pats, shq(n.id+":--"+f.long))
				if f.short != "" {
					pats = append(pats, shq(n.id+":-"+f.short))
				}
			}
		}
		if len(pats) > 0 {
			fmt.Fprintf(&b, "      case %s\n        set skip 1\n", strings.Join(pats, " "))
		}
	}
	b.WriteString("    end\n  end\n  echo $cmd\nend\n\n")
	fmt.Fprintf(&b, "complete -c %s -f\n", root.Name)
	for _, n := range nodes {
		cond := fmt.Sprintf("-n %s", shq(fmt.Sprintf("test (%s) = %s", fn, n.id)))
		for _, s := range n.cmd.Subcommands {
			fmt.Fprintf(&b, "complete -c %s %s -a %s -d %s\n", root.Name, cond, shq(s.Name), shq(s.Short))
		}
		if len(n.cmd.Args) > 0 {
			fmt.Fprintf(&b, "complete -c %s %s -a %s\n", root.Name, cond, shq(strings.Join(n.cmd.Args, " ")))
		}
		for _, f := range flagsOf(n.cmd) {
			line := fmt.Sprintf("complete -c %s %s -l %s", root.Name, cond, shq(f.long))
			if f.short != "" {
				line += " -s " + shq(f.short)
			}
			if f.value {
				switch {
				case len(f.choices) > 0:
					line += " -x -a " + shq(strings.Join(f.choices, " "))
				case f.dirs:
					line += " -x -a '(__fish_complete_directories)'"
				default:
					line += " -r -F"
				}
			}
			line += " -d " + shq(f.usage)
			b.WriteString(line + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Names lists every command path of the tree, "root sub", for tests and help.
func Names(root *Command) []string {
	var out []string
	var visit func(prefix string, c *Command)
	visit = func(prefix string, c *Command) {
		out = append(out, strings.TrimSpace(prefix+" "+c.Name))
		for _, s := range c.Subcommands {
			visit(strings.TrimSpace(prefix+" "+c.Name), s)
		}
	}
	visit("", root)
	slices.Sort(out)
	return out
}
