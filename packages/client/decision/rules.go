package decision

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/permissions"
)

// ProviderLocalRules names verdicts of the client's copy of the rules provider.
const ProviderLocalRules = "local-rules"

// Rule actions.
const (
	ActionDeny  = "deny"
	ActionAllow = "allow"
)

// Rule scopes: what the patterns are matched against.
const (
	// ScopeCommand matches the whole Bash command line as written.
	ScopeCommand = "command"
	// ScopeSubcommand matches each normalized simple command (rule text:
	// unquoted words joined by spaces, then redirections); every pattern
	// must match the same subcommand.
	ScopeSubcommand = "subcommand"
	// ScopeWritePath matches each resolved write path, with the home
	// directory written as "~" (Bash and file-editing tools).
	ScopeWritePath = "write_path"
	// ScopeHost matches each contacted network host (Bash and WebFetch).
	ScopeHost = "host"
	// ScopeWorkdirEdit (allow only, no patterns) matches file edits whose
	// targets are all inside the working directories.
	ScopeWorkdirEdit = "workdir_edit"
)

// Risk categories (harness.decision.v1.RiskCategory without the prefix, lower case).
const (
	CatDestructive         = "destructive"
	CatRemoteCodeExecution = "remote_code_execution"
	CatSecretExfiltration  = "secret_exfiltration"
	CatProductionChange    = "production_change"
	CatGitHistoryRewrite   = "git_history_rewrite"
	CatPrivilegeEscalation = "privilege_escalation"
	CatNetworkEgress       = "network_egress"
	CatOutOfScope          = "out_of_scope"
	CatPackageInstall      = "package_install"
)

// Rule is one entry of the rules table. The JSON shape is meant to be
// synced with the server's rules provider export.
type Rule struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Scope  string `json:"scope"`
	// Tools limits the rule to these tools (default: Bash for command and
	// subcommand scopes, every tool otherwise).
	Tools []string `json:"tools,omitempty"`
	// Patterns are RE2 expressions; all must match (the same subcommand for
	// the subcommand scope).
	Patterns []string `json:"patterns,omitempty"`
	// Unless disables the rule when any of these match the same value.
	Unless []string `json:"unless,omitempty"`
	// Branch, when set, must match the current git branch.
	Branch     string   `json:"branch,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Reason     string   `json:"reason"`

	pats, unless []*regexp.Regexp
	branch       *regexp.Regexp
}

// Compile prepares the rule's expressions.
func (r *Rule) Compile() error {
	comp := func(src []string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, s := range src {
			re, err := regexp.Compile(s)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %w", r.ID, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	var err error
	if r.pats, err = comp(r.Patterns); err != nil {
		return err
	}
	if r.unless, err = comp(r.Unless); err != nil {
		return err
	}
	if r.Branch != "" {
		if r.branch, err = regexp.Compile(r.Branch); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	if r.Action != ActionDeny && r.Action != ActionAllow {
		return fmt.Errorf("rule %s: unknown action %q", r.ID, r.Action)
	}
	return nil
}

func (r *Rule) appliesTo(tool string) bool {
	if len(r.Tools) == 0 {
		switch r.Scope {
		case ScopeCommand, ScopeSubcommand:
			return tool == "Bash"
		}
		return true
	}
	for _, t := range r.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// matches reports whether all patterns and none of the exceptions match s.
func (r *Rule) matches(s string) bool {
	for _, re := range r.pats {
		if !re.MatchString(s) {
			return false
		}
	}
	for _, re := range r.unless {
		if re.MatchString(s) {
			return false
		}
	}
	return true
}

// Shared pattern fragments.
const (
	sep       = `(?:^|[\s;&|()"'` + "`" + `])`
	end       = `(?:\s|$)`
	netTool   = sep + `(?:curl|wget|nc|ncat|netcat|socat|scp|rsync|sftp|ftp|telnet|ssh|http|https|xh|aria2c)` + end
	gitPush   = `^git(?:\s+-\S+(?:\s+\S+)?)*\s+push` + end
	forceFlag = `\s(?:-[a-zA-Z]*f[a-zA-Z]*|--force|--force-with-lease(?:=\S*)?|--force-if-includes)` + end + `|\s\+\S+`
	dbClient  = sep + `(?:psql|pgcli|mysql|mycli|mariadb|sqlite3|duckdb|mongo|mongosh|cockroach|clickhouse|clickhouse-client|sqlcmd|redis-cli|snowsql|usql|bq)` + end
	rmTargets = `\s(?:/|/\*|~|~/|~/\*|\$HOME|\$HOME/|\$HOME/\*|\$\{HOME\}|\$\{HOME\}/|\$\{HOME\}/\*|\.|\./|\.\.|\.\./|\*|\.\*|/(?:bin|boot|dev|etc|home|lib|lib64|opt|private|proc|root|sbin|srv|sys|usr|var|System|Library|Applications|Users|Volumes)(?:/|/\*)?|/(?:Users|home)/[^/\s]+/?|/(?:Users|home)/[^/\s]+/\*)` + end
	credFiles = `(?:\.ssh/(?:id_[A-Za-z0-9_]+|[^\s/]*_key)\b|\.aws/(?:credentials|config)\b|\.netrc\b|\.npmrc\b|\.pypirc\b|\.docker/config\.json|\.kube/config\b|\.git-credentials\b|\.config/gcloud\b|\.config/gh/hosts\.yml|\.gnupg/|(?:^|[\s/'"=])\.env(?:\.[\w-]+)?(?:$|[\s'";|&)])|credentials\.json\b|/etc/shadow\b|\.vault-token\b|security\s+find-(?:generic|internet)-password)`
)

// DefaultRules is the built-in table: hard denies first, then fast allows.
var DefaultRules = []Rule{
	// ---- remote code execution ----
	{ID: "pipe-to-shell", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatRemoteCodeExecution},
		Patterns: []string{sep + `(?:curl|wget|fetch|aria2c|http|xh)\b[^|;&]*\|\s*(?:sudo\s+(?:-\S+\s+)*)?(?:env\s+(?:\S+=\S*\s+)*)?(?:/(?:usr/)?bin/)?(?:ba|z|da|k|fi|c|tc)?sh` + `(?:\s|$|[;&|)"'])`},
		Reason:   "Piping a download straight into a shell runs unreviewed remote code."},
	{ID: "pipe-to-interpreter", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatRemoteCodeExecution},
		Patterns: []string{sep + `(?:curl|wget|fetch|aria2c|http|xh)\b[^|;&]*\|\s*(?:sudo\s+)?(?:python[0-9.]*|perl|ruby|node|php|bun|deno)\s*(?:-\s*)?(?:$|[;&|)"'])`},
		Reason:   "Piping a download into an interpreter runs unreviewed remote code."},
	{ID: "shell-from-download", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatRemoteCodeExecution},
		Patterns: []string{`(?:(?:^|[\s;&|(])(?:ba|z|da|k)?sh\s+(?:-\S+\s+)*(?:<\(|["']?\$\()|(?:^|[\s;&|(])(?:source|\.|eval)\s+(?:<\(|["']?\$\())\s*(?:curl|wget)\b`},
		Reason:   "Running a script fetched from the network executes unreviewed remote code."},

	// ---- destructive deletes ----
	{ID: "rm-rf-dangerous-target", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^rm\s`, `\s(?:-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)` + end, rmTargets},
		Reason:   "Recursive delete of the filesystem root, the home directory, a system directory or the whole working tree."},
	{ID: "rm-no-preserve-root", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^rm\s.*--no-preserve-root`}, Reason: "rm --no-preserve-root can delete the entire filesystem."},
	{ID: "find-delete-broad", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^find\s+(?:/|~|\$HOME|/Users|/home)(?:/)?\s.*(?:-delete|-exec\s+rm)` + end},
		Reason:   "Deleting files across the whole filesystem or home directory."},
	{ID: "dd-to-device", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^dd\s.*\bof=/dev/(?:sd|hd|nvme|disk|rdisk|xvd|vd|mmcblk|mapper|md|loop)`},
		Reason:   "dd onto a block device overwrites a disk."},
	{ID: "format-disk", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^(?:mkfs(?:\.\w+)?|mke2fs|mkswap|newfs(?:_\w+)?|wipefs|fdisk|sfdisk|gdisk|parted|diskutil\s+(?:erase\w*|partitionDisk|zeroDisk|secureErase|reformat))` + end},
		Reason:   "Formatting or repartitioning a disk destroys its data."},
	{ID: "write-block-device", Action: ActionDeny, Scope: ScopeWritePath, Categories: []string{CatDestructive},
		Patterns: []string{`^/dev/(?:sd|hd|nvme|disk|rdisk|xvd|vd|mmcblk)`}, Reason: "Writing directly to a block device overwrites a disk."},
	{ID: "fork-bomb", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatDestructive},
		Patterns: []string{`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`}, Reason: "A fork bomb exhausts the machine."},
	{ID: "shutdown", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^(?:shutdown|reboot|halt|poweroff|init\s+[06]|systemctl\s+(?:poweroff|reboot|halt|kexec))` + end},
		Reason:   "Shutting down or rebooting the machine."},
	{ID: "crontab-remove", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive},
		Patterns: []string{`^crontab\s+(?:-\S+\s+)*-[a-zA-Z]*r[a-zA-Z]*` + end}, Reason: "crontab -r deletes all scheduled jobs."},

	// ---- git history ----
	{ID: "force-push-main", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatGitHistoryRewrite},
		Patterns: []string{gitPush, forceFlag, `(?:\s|:|/|\+)(?:main|master)` + end},
		Reason:   "Force-pushing to main/master rewrites shared history."},
	{ID: "force-push-current-main", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatGitHistoryRewrite}, Branch: `^(?:main|master)$`,
		Patterns: []string{gitPush, forceFlag, `^git(?:\s+-\S+)*\s+push(?:\s+-\S+)*(?:\s+[\w./-]+)?(?:\s+-\S+)*$`},
		Reason:   "Force-pushing the current branch (main/master) rewrites shared history."},
	{ID: "delete-remote-main", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatGitHistoryRewrite, CatDestructive},
		Patterns: []string{gitPush, `(?:\s(?:--delete|-d)\s+(?:\S+\s+)?|\s:)(?:main|master)` + end},
		Reason:   "Deleting the remote main/master branch."},

	// ---- infrastructure / production ----
	{ID: "terraform-destroy", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange, CatDestructive},
		Patterns: []string{`^(?:terraform|tofu|terragrunt)\s(?:.*\s)?(?:destroy|apply\s(?:.*\s)?-destroy)` + end},
		Reason:   "terraform destroy tears down infrastructure."},
	{ID: "terraform-auto-approve", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange},
		Patterns: []string{`^(?:terraform|tofu|terragrunt)\s(?:.*\s)?(?:apply|run-all\s+apply)` + end, `\s--?auto-approve(?:=true)?` + end},
		Reason:   "terraform apply -auto-approve changes infrastructure without review."},
	{ID: "kubectl-destructive", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange, CatDestructive},
		Patterns: []string{`^kubectl\s(?:.*\s)?(?:delete|drain|cordon|taint)` + end},
		Reason:   "Destructive kubectl operation against a cluster."},
	{ID: "kubectl-scale-zero", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange},
		Patterns: []string{`^kubectl\s(?:.*\s)?scale\s.*--replicas[= ]0` + end}, Reason: "Scaling a workload to zero takes it down."},
	{ID: "kubectl-replace-force", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange, CatDestructive},
		Patterns: []string{`^kubectl\s(?:.*\s)?replace\s`, `\s--force` + end}, Reason: "kubectl replace --force deletes and recreates resources."},
	{ID: "helm-destructive", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange, CatDestructive},
		Patterns: []string{`^helm\s(?:.*\s)?(?:uninstall|delete|del|rollback)` + end}, Reason: "Destructive helm operation against a cluster."},
	{ID: "cloud-delete", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatProductionChange, CatDestructive},
		Patterns: []string{`^(?:aws\s+s3\s+(?:rm\s.*--recursive|rb\s.*--force)|aws\s(?:.*\s)?(?:delete-[\w-]+|terminate-instances)|gsutil\s+(?:-m\s+)?rm\s+-[a-zA-Z]*r|gcloud\s(?:.*\s)?delete|az\s(?:.*\s)?delete)` + end},
		Reason:   "Deleting cloud resources."},

	// ---- databases ----
	{ID: "sql-drop", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatDestructive, CatProductionChange},
		Patterns: []string{`(?i)(?:\bdrop\s+(?:database|schema|table)\b|\btruncate\s+(?:table\s+)?[a-z_"]|\bdropDatabase\s*\(|\bflushall\b|\bflushdb\b)`, dbClient},
		Reason:   "Dropping or truncating database objects destroys data."},
	{ID: "db-drop-tool", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatDestructive, CatProductionChange},
		Patterns: []string{`^(?:dropdb|dropuser|(?:bin/)?(?:rails|rake)\s+db:(?:drop|reset|purge|schema:load)|(?:npx\s+|bunx\s+|pnpm\s+(?:exec\s+|dlx\s+)?|yarn\s+)?prisma\s+(?:migrate\s+reset|db\s+push\s.*--force-reset)|(?:python[0-9.]*\s+)?manage\.py\s+(?:flush|reset_db))` + end},
		Reason:   "Dropping or resetting a database destroys data."},

	// ---- secrets ----
	{ID: "credentials-to-network", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatSecretExfiltration},
		Patterns: []string{credFiles, netTool}, Reason: "Reading credential files in a command that contacts the network can leak secrets."},
	{ID: "env-to-network", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatSecretExfiltration},
		Patterns: []string{`(?:^|[;&|(]\s*)(?:env|printenv|set|export\s+-p)\s*\|`, netTool}, Reason: "Sending the environment (which holds tokens) over the network."},
	{ID: "cloud-metadata", Action: ActionDeny, Scope: ScopeCommand, Categories: []string{CatSecretExfiltration},
		Patterns: []string{`169\.254\.169\.254|metadata\.google\.internal|100\.100\.100\.200|fd00:ec2::254`}, Reason: "Fetching cloud instance metadata exposes machine credentials."},
	{ID: "cloud-metadata-host", Action: ActionDeny, Scope: ScopeHost, Tools: []string{"WebFetch", "Bash"}, Categories: []string{CatSecretExfiltration},
		Patterns: []string{`^(?:169\.254\.169\.254|metadata\.google\.internal|100\.100\.100\.200)$`}, Reason: "Fetching cloud instance metadata exposes machine credentials."},

	// ---- privilege escalation ----
	{ID: "sudo", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^(?:sudo|doas|pkexec|su)` + end}, Reason: "Running commands as root needs the user, not auto mode."},
	{ID: "chmod-world-writable", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^chmod\s+(?:-\S+\s+)*(?:0?777|[ugoa]*=rwx|a\+rwx|ugo\+rwx|\+rwx|o\+w|a\+w)` + end}, Reason: "chmod 777 makes files writable by everyone."},
	{ID: "chmod-setuid", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^chmod\s+(?:-\S+\s+)*(?:[ugoa]*\+[rwxt]*s[rwxt]*|[2-7][0-7]{3})` + end}, Reason: "Setting setuid/setgid bits escalates privileges."},
	{ID: "chown-root", Action: ActionDeny, Scope: ScopeSubcommand, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^chown\s+(?:-\S+\s+)*(?:root|0)(?::\S*)?\s`}, Reason: "Changing file ownership to root."},
	{ID: "shell-rc-write", Action: ActionDeny, Scope: ScopeWritePath, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^(?:~/\.(?:bashrc|bash_profile|bash_login|bash_logout|profile|zshrc|zprofile|zshenv|zlogin|zlogout|cshrc|tcshrc|kshrc|inputrc)|~/\.config/fish/.*|/etc/(?:profile|bashrc|bash\.bashrc|zshrc|zprofile|zshenv)(?:\.d/.*)?)$`},
		Reason:   "Editing shell startup files changes what runs in every future shell."},
	{ID: "ssh-config-write", Action: ActionDeny, Scope: ScopeWritePath, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^(?:~/\.ssh(?:/.*)?|/etc/ssh/.*)$`}, Reason: "Changing SSH configuration or keys."},
	{ID: "system-path-write", Action: ActionDeny, Scope: ScopeWritePath, Categories: []string{CatPrivilegeEscalation, CatDestructive},
		Patterns: []string{`^/(?:etc|bin|sbin|usr|boot|lib|lib64|System|Library|private/etc|var/root)(?:/.*)?$`}, Reason: "Writing to system directories."},
	{ID: "persistence-write", Action: ActionDeny, Scope: ScopeWritePath, Categories: []string{CatPrivilegeEscalation},
		Patterns: []string{`^~/(?:Library/LaunchAgents|\.config/autostart|\.config/systemd/user)/.*$`}, Reason: "Installing programs that start automatically."},

	// ---- fast allow ----
	{ID: "edit-in-workdir", Action: ActionAllow, Scope: ScopeWorkdirEdit, Reason: "File edit inside the working directories."},
	{ID: "go-toolchain", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^go\s+(?:test|vet|build|list|version|doc|fmt|mod\s+(?:tidy|download|verify|why|graph))` + end}, Unless: []string{`\s-(?:exec|toolexec)\b`, `\s-o\s+/`},
		Reason: "Go build/test tooling."},
	{ID: "go-env-read", Action: ActionAllow, Scope: ScopeSubcommand, Patterns: []string{`^go\s+env(?:\s+[A-Z0-9_]+)*$`}, Reason: "Reading Go environment."},
	{ID: "go-lint", Action: ActionAllow, Scope: ScopeSubcommand, Patterns: []string{`^(?:gofmt\s+-[lds]|golangci-lint\s+run|staticcheck)` + end}, Reason: "Go linters."},
	{ID: "js-test", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^(?:(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?(?:test|tests|lint|typecheck|type-check|check|build|format:check)(?::[\w:-]+)?|(?:npx|pnpm\s+exec|yarn|bunx)\s+(?:vitest|jest|mocha|tsc|eslint|playwright\s+test)|vitest|jest|tsc|eslint|bun\s+test|deno\s+(?:test|check|lint))` + end},
		Reason:   "JavaScript test, lint or build run."},
	{ID: "py-test", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^(?:(?:python[0-9.]*|uv\s+run|poetry\s+run)\s+-m\s+(?:pytest|unittest|mypy)|(?:uv\s+run\s+|poetry\s+run\s+)?(?:pytest|mypy|ruff\s+check|ruff\s+format\s+--check|flake8|pylint|tox))` + end},
		Reason:   "Python test or lint run."},
	{ID: "rust-test", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^cargo\s+(?:test|check|build|clippy|nextest\s+run|fmt\s+--check|doc|tree)` + end}, Reason: "Cargo build/test run."},
	{ID: "make-test", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^make\s+(?:-\S+\s+)*(?:test|tests|check|lint|vet|fmt-check)` + end}, Reason: "make test/check target."},
	{ID: "jvm-dotnet-test", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^(?:(?:mvn|\./mvnw)\s+(?:-\S+\s+)*(?:test|verify|compile)|(?:gradle|\./gradlew)\s+(?:test|check|build)|dotnet\s+(?:test|build)|mix\s+test|(?:bundle\s+exec\s+)?(?:rspec|rake\s+test)|swift\s+(?:test|build))` + end},
		Reason:   "Build or test run."},
	{ID: "version-query", Action: ActionAllow, Scope: ScopeSubcommand,
		Patterns: []string{`^[\w.+-]+\s+(?:--version|-version|-V|version)$`}, Unless: []string{`^(?:sudo|doas|su|rm|dd)\s`}, Reason: "Version query."},
}

// Rules is the local rules provider. The zero value is not usable; use
// NewRules or LocalRules.
type Rules struct {
	deny, allow []*Rule
	// Git supplies the git facts (default DefaultGitInfo).
	Git GitInfo
}

// NewRules compiles a rules table.
func NewRules(table []Rule) (*Rules, error) {
	r := &Rules{Git: DefaultGitInfo}
	for i := range table {
		rule := table[i]
		if err := rule.Compile(); err != nil {
			return nil, err
		}
		if rule.Action == ActionDeny {
			r.deny = append(r.deny, &rule)
		} else {
			r.allow = append(r.allow, &rule)
		}
	}
	return r, nil
}

var (
	localOnce  sync.Once
	localRules *Rules
)

// LocalRules returns the compiled DefaultRules.
func LocalRules() *Rules {
	localOnce.Do(func() {
		var err error
		if localRules, err = NewRules(DefaultRules); err != nil {
			panic(err)
		}
	})
	return localRules
}

// Decide implements permissions.Decider.
func (r *Rules) Decide(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, error) {
	return r.Evaluate(req, ComputeFacts(ctx, req, r.Git)), nil
}

// Evaluate applies the table: the first matching deny rule decides DENY;
// then ALLOW when every part of the call is fast-allowed; otherwise ASK.
func (r *Rules) Evaluate(req permissions.AutoRequest, f Facts) permissions.AutoResult {
	if rule := r.matchDeny(req, f); rule != nil {
		return permissions.AutoResult{Behavior: permissions.Deny, Reason: rule.Reason, Categories: rule.Categories, Provider: ProviderLocalRules, Confidence: 1}
	}
	if rule := r.fastAllow(req, f); rule != nil {
		return permissions.AutoResult{Behavior: permissions.Allow, Reason: rule.Reason, Provider: ProviderLocalRules, Confidence: 1}
	}
	return permissions.AutoResult{Behavior: permissions.Ask, Reason: "No local rule covers this action.", Provider: ProviderLocalRules}
}

// RuleFor returns the id of the rule that decides the call ("" for ASK).
func (r *Rules) RuleFor(req permissions.AutoRequest, f Facts) string {
	if rule := r.matchDeny(req, f); rule != nil {
		return rule.ID
	}
	if rule := r.fastAllow(req, f); rule != nil {
		return rule.ID
	}
	return ""
}

func (r *Rules) matchDeny(req permissions.AutoRequest, f Facts) *Rule {
	paths := displayPaths(f.WritePaths, req.Home)
	for _, rule := range r.deny {
		if !rule.appliesTo(req.ToolName) {
			continue
		}
		if rule.branch != nil && !rule.branch.MatchString(f.GitBranch) {
			continue
		}
		switch rule.Scope {
		case ScopeCommand:
			if req.Command != "" && rule.matches(req.Command) {
				return rule
			}
		case ScopeSubcommand:
			for _, s := range f.Subcommands {
				if rule.matches(s) {
					return rule
				}
			}
		case ScopeWritePath:
			for _, p := range paths {
				if rule.matches(p) {
					return rule
				}
			}
		case ScopeHost:
			for _, h := range f.NetworkHosts {
				if rule.matches(h) {
					return rule
				}
			}
		}
	}
	return nil
}

// fastAllow returns the allow rule that covers the whole call, or nil.
func (r *Rules) fastAllow(req permissions.AutoRequest, f Facts) *Rule {
	workDirs := req.WorkDirs
	if len(workDirs) == 0 {
		workDirs = append([]string{req.Cwd}, req.AdditionalDirs...)
	}
	inWork := func() bool {
		for _, p := range f.WritePaths {
			if !permissions.WithinAny(p, workDirs) || permissions.IsProtectedPath(p, workDirs, req.Home) {
				return false
			}
		}
		return true
	}
	switch {
	case permissions.IsEditTool(req.ToolName):
		if len(f.WritePaths) == 0 || !inWork() {
			return nil
		}
		for _, rule := range r.allow {
			if rule.Scope == ScopeWorkdirEdit && rule.appliesTo(req.ToolName) {
				return rule
			}
		}
	case req.ToolName == "Bash":
		if f.Unsafe || len(f.Subcommands) == 0 || !inWork() {
			return nil
		}
		var decided *Rule
		for _, raw := range permissions.SplitCommand(req.Command) {
			c := raw.Normalize()
			if permissions.IsReadOnlyCommand(c) {
				continue
			}
			rule := r.allowSubcommand(ruleText(c))
			if rule == nil {
				return nil
			}
			decided = rule
		}
		if decided == nil {
			return &Rule{ID: "read-only", Reason: "Read-only command."}
		}
		return decided
	}
	return nil
}

func (r *Rules) allowSubcommand(text string) *Rule {
	for _, rule := range r.allow {
		if rule.Scope == ScopeSubcommand && rule.matches(text) {
			return rule
		}
	}
	return nil
}

// displayPaths writes paths under home as "~/...".
func displayPaths(paths []string, home string) []string {
	out := make([]string, 0, len(paths))
	home = strings.TrimSuffix(filepath.ToSlash(home), "/")
	for _, p := range paths {
		p = filepath.ToSlash(p)
		switch {
		case home != "" && p == home:
			p = "~"
		case home != "" && strings.HasPrefix(p, home+"/"):
			p = "~/" + strings.TrimPrefix(p, home+"/")
		}
		out = append(out, p)
	}
	return out
}
