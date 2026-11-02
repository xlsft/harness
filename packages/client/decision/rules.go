package decision

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/permissions"
)

// ProviderLocalRules names verdicts of the client's copy of the rules provider.
const ProviderLocalRules = "local-rules"

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

// Rules is the local rules provider: EvaluateRules (the port of the
// server's rules.ts in rulesengine.go) fed exactly what the server's rules
// provider receives for the same call (see Input).
type Rules struct {
	// Options are the rule options (zero value: the server's defaults). Set
	// the field before use, or call SetOptions while the rules are in use.
	Options RulesOptions
	// Git supplies the git facts (default DefaultGitInfo).
	Git GitInfo

	mu sync.RWMutex
}

// SetOptions replaces the rule options (safe while the rules are in use).
func (r *Rules) SetOptions(opts RulesOptions) {
	r.mu.Lock()
	r.Options = opts
	r.mu.Unlock()
}

// CurrentOptions returns the rule options in effect.
func (r *Rules) CurrentOptions() RulesOptions {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Options
}

// NewRules returns a rules provider with the given options.
func NewRules(opts RulesOptions) *Rules {
	return &Rules{Options: opts, Git: DefaultGitInfo}
}

var (
	localOnce  sync.Once
	localRules *Rules
)

// LocalRules returns the rules provider with the server's default options.
func LocalRules() *Rules {
	localOnce.Do(func() { localRules = NewRules(RulesOptions{}) })
	return localRules
}

// Decide implements permissions.Decider.
func (r *Rules) Decide(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, error) {
	return r.Evaluate(req, ComputeFacts(ctx, req, r.Git)), nil
}

// Evaluate returns the verdict of the rules for a call and its facts.
func (r *Rules) Evaluate(req permissions.AutoRequest, f Facts) permissions.AutoResult {
	res := r.Result(req, f)
	out := permissions.AutoResult{Reason: res.Reason, Categories: append([]string(nil), res.Categories...), Provider: ProviderLocalRules}
	switch res.Decision {
	case RuleDeny:
		out.Behavior, out.Confidence = permissions.Deny, 1
	case RuleAllow:
		out.Behavior, out.Confidence = permissions.Allow, 1
	default:
		out.Behavior = permissions.Ask
	}
	return out
}

// Result returns the full rules result (with the deciding rule id).
func (r *Rules) Result(req permissions.AutoRequest, f Facts) RulesResult {
	in, ok := Input(req, f)
	if !ok {
		return ask("Tool call is not a valid decision request", "ask.bad_input")
	}
	return EvaluateRules(in, r.CurrentOptions())
}

// RuleFor returns the id of the rule that decides the call (as the server
// names it: "rce.pipe_to_shell", "allow.safe_command", "ask.default", ...).
func (r *Rules) RuleFor(req permissions.AutoRequest, f Facts) string {
	return r.Result(req, f).Rule
}

// Server-side limits of a DecideRequest (packages/core/src/decision/service.ts,
// validateDecideRequest).
const (
	maxRequestDirs       = 64
	maxRequestPathLen    = 4096
	maxRequestSubcmds    = 256
	maxRequestSubcmdLen  = 8192
	maxRequestWritePaths = 256
	maxRequestHosts      = 64
	maxRequestHostLen    = 512
	maxRequestBranchLen  = 256
)

// Input builds the rules input the server's rules provider sees for the
// request BuildRequest sends: the same tool name, input, cwd, additional
// directories and facts, after the server's request validation (trimmed tool
// name, list and length limits). ok is false when the server would reject
// the request (no tool name, input not a JSON object).
func Input(req permissions.AutoRequest, f Facts) (RulesInput, bool) {
	tool := strings.TrimSpace(req.ToolName)
	input := req.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var obj map[string]any
	if tool == "" || json.Unmarshal(input, &obj) != nil || obj == nil {
		return RulesInput{}, false
	}
	return RulesInput{
		ToolName:              tool,
		Input:                 input,
		Cwd:                   jsSlice(req.Cwd, maxRequestPathLen),
		AdditionalDirectories: limitStrings(req.AdditionalDirs, maxRequestDirs, maxRequestPathLen),
		Facts: RulesFacts{
			Subcommands:   limitStrings(f.Subcommands, maxRequestSubcmds, maxRequestSubcmdLen),
			WritePaths:    limitStrings(f.WritePaths, maxRequestWritePaths, maxRequestPathLen),
			NetworkHosts:  limitStrings(f.NetworkHosts, maxRequestHosts, maxRequestHostLen),
			InsideGitRepo: f.InsideGitRepo,
			GitBranch:     jsSlice(f.GitBranch, maxRequestBranchLen),
		},
	}, true
}

// limitStrings keeps the last max items, each cut to each UTF-16 units.
func limitStrings(v []string, max, each int) []string {
	if len(v) > max {
		v = v[len(v)-max:]
	}
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = jsSlice(s, each)
	}
	return out
}
