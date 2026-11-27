package settings

import (
	"encoding/json"
	"math"
)

// Project runs (docs/design/coordinator.md): how many workers a run runs at once and what it may spend, when the run
// names nothing. A policy document's values are ceilings over the user's, and disableProjectRuns turns runs off.
const (
	KeyProjectMaxWorkers  = "projectMaxWorkers"
	KeyProjectBudgetUSD   = "projectBudgetUsd"
	KeyDisableProjectRuns = "disableProjectRuns"

	DefaultProjectMaxWorkers = 4
	MaxProjectMaxWorkers     = 32
)

// ValidProjectMaxWorkers reports whether n can be Settings.ProjectMaxWorkers.
func ValidProjectMaxWorkers(n int) bool { return n >= 1 && n <= MaxProjectMaxWorkers }

// ValidProjectBudget reports whether v can be Settings.ProjectBudgetUSD (0: none).
func ValidProjectBudget(v float64) bool { return v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

func (s *Settings) parseProject(src Source, raw map[string]json.RawMessage, l *layer) {
	if l.projectWorkers = s.integer(src, raw, KeyProjectMaxWorkers); l.projectWorkers != nil && !ValidProjectMaxWorkers(*l.projectWorkers) {
		s.problem(src, KeyProjectMaxWorkers, "must be between 1 and %d", MaxProjectMaxWorkers)
		l.projectWorkers = nil
	}
	if v, ok := raw[KeyProjectBudgetUSD]; ok && string(v) != "null" {
		var f float64
		if json.Unmarshal(v, &f) != nil || !ValidProjectBudget(f) {
			s.problem(src, KeyProjectBudgetUSD, "must be a number of USD, 0 or more")
		} else {
			l.projectBudget = &f
		}
	}
	if src.Level == LevelPolicy {
		if b := s.boolean(src, raw, KeyDisableProjectRuns); b != nil && *b {
			l.projectOff = true
		}
	}
}

// mergeProject takes one layer, highest precedence first: the first value of a non-policy level wins, policy values cap.
func (s *Settings) mergeProject(l *layer) {
	if l.src.Level == LevelPolicy {
		s.ProjectRunsDisabled = s.ProjectRunsDisabled || l.projectOff
		if n := l.projectWorkers; n != nil && (s.ProjectPolicyMaxWorkers == 0 || *n < s.ProjectPolicyMaxWorkers) {
			s.ProjectPolicyMaxWorkers = *n
		}
		if b := l.projectBudget; b != nil && *b > 0 && (s.ProjectPolicyBudgetUSD == 0 || *b < s.ProjectPolicyBudgetUSD) {
			s.ProjectPolicyBudgetUSD = *b
		}
		return
	}
	if n := l.projectWorkers; n != nil && !s.projectWorkersSet {
		s.ProjectMaxWorkers, s.projectWorkersSet = *n, true
	}
	if b := l.projectBudget; b != nil && !s.projectBudgetSet {
		s.ProjectBudgetUSD, s.projectBudgetSet = *b, true
	}
}

// ProjectLimits is the effective limits of a run asking for workers and budget (0: the defaults): the user's
// values, then the policy's ceilings.
func (s *Settings) ProjectLimits(workers int, budgetUSD float64) (int, float64) {
	if workers <= 0 {
		workers = s.ProjectMaxWorkers
	}
	if workers <= 0 {
		workers = DefaultProjectMaxWorkers
	}
	workers = min(workers, MaxProjectMaxWorkers)
	if p := s.ProjectPolicyMaxWorkers; p > 0 && workers > p {
		workers = p
	}
	if budgetUSD <= 0 {
		budgetUSD = s.ProjectBudgetUSD
	}
	if p := s.ProjectPolicyBudgetUSD; p > 0 && (budgetUSD == 0 || budgetUSD > p) {
		budgetUSD = p
	}
	return workers, budgetUSD
}
