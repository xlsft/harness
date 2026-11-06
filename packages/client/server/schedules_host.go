package server

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/schedule"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Schedules and always-on hosts (docs/design/hosts.md, decision 19). A schedule can name a host that runs it while its
// machine is away. The machine and the host each ask the server for a due time, so that the machine's catch-up and the host
// don't both run it.

// HostMode is what an always-on host adds to the scheduler.
type HostMode struct {
	// HostID is this host's id, "" until it registered.
	HostID func() string
	// Prepare makes a checkout for a run: its folder, and what to call once the session exists.
	Prepare func(ctx context.Context, repositoryKey string) (string, func(context.Context, string), error)
	// Claim asks the server for a due time.
	Claim func(ctx context.Context, scheduleID, dueAt string) (bool, error)
	// Grace is how long a machine is away before its schedules are taken (default two minutes).
	Grace time.Duration
	// ListMachines overrides how the user's machines are looked at, device to online (tests). Default: the relay.
	ListMachines func(ctx context.Context) (map[string]bool, error)
}

// ScheduleClaim is the server's answer to "who runs this time" for an ordinary machine (nil: no server to ask).
type ScheduleClaim func(ctx context.Context, scheduleID, dueAt string) (bool, error)

const (
	scheduleClaimTimeout  = 5 * time.Second
	scheduleMachinesEvery = 30 * time.Second
)

// machineSeen remembers when each machine was last seen up, for adopts.
type machineSeen struct {
	online atomic.Pointer[map[string]bool]
	seen   atomic.Pointer[map[string]time.Time]
}

// adopts says whether this host takes a task whose machine is another one.
func (h *scheduleHost) adopts(t schedule.Task) bool {
	m := h.s.HostMode
	if m == nil || t.DeviceLocal || t.AwayHostID == "" || t.RepositoryKey == "" || t.DeviceID == h.s.DeviceID {
		return false
	}
	if m.HostID == nil || t.AwayHostID != m.HostID() {
		return false
	}
	return h.machineAway(t.DeviceID)
}

// machineAway reads the last look at the user's machines (refreshMachines). Unknown counts as away only once a look
// succeeded: a host that can't see the relay takes nothing.
func (h *scheduleHost) machineAway(deviceID string) bool {
	online := h.machines.online.Load()
	if online == nil {
		return false
	}
	if (*online)[deviceID] {
		return false
	}
	seen := h.machines.seen.Load()
	grace := h.s.HostMode.Grace
	if grace <= 0 {
		grace = 2 * time.Minute
	}
	if seen != nil {
		if at, ok := (*seen)[deviceID]; ok && h.s.now().Sub(at) < grace {
			return false
		}
	}
	return true
}

// refreshMachines takes a new look at which machines are up.
func (h *scheduleHost) refreshMachines(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, scheduleClaimTimeout)
	defer cancel()
	if list := h.s.HostMode.ListMachines; list != nil {
		machines, err := list(cctx)
		if err != nil {
			return
		}
		online := map[string]bool{}
		seen := map[string]time.Time{}
		for id, up := range machines {
			online[id] = up
			if up {
				seen[id] = h.s.now()
			}
		}
		h.machines.online.Store(&online)
		h.machines.seen.Store(&seen)
		return
	}
	viewer := h.s.Relay
	if viewer == nil {
		return
	}
	res, err := relayCall(cctx, viewer, relayv1connect.RelayServiceClient.ListHosts, &relayv1.ListHostsRequest{})
	if err != nil {
		h.s.log().Debug("schedules: looking at the machines failed", "err", err)
		return
	}
	hosts := res.GetHosts()
	online := map[string]bool{}
	seen := map[string]time.Time{}
	if prev := h.machines.seen.Load(); prev != nil {
		for k, v := range *prev {
			seen[k] = v
		}
	}
	now := h.s.now()
	for _, host := range hosts {
		if host.GetOnline() {
			online[host.GetDeviceId()] = true
			seen[host.GetDeviceId()] = now
		} else if _, known := seen[host.GetDeviceId()]; !known {
			if at, err := time.Parse(time.RFC3339, host.GetLastSeenAt()); err == nil {
				seen[host.GetDeviceId()] = at
			}
		}
	}
	h.machines.online.Store(&online)
	h.machines.seen.Store(&seen)
}

// claimRun asks the server whether this machine runs a due time. A machine whose own schedule it is runs it if the server
// can't be reached; a host doesn't.
func (h *scheduleHost) claimRun(t schedule.Task, due time.Time) bool {
	mine := t.DeviceLocal || (t.DeviceID != "" && t.DeviceID == h.s.DeviceID)
	var claim ScheduleClaim
	if m := h.s.HostMode; m != nil {
		claim = m.Claim
	} else {
		claim = h.s.ScheduleClaim
	}
	if claim == nil {
		return mine
	}
	// Each machine counts an interval from its own last run, so the same slot is a matter of rounding.
	if every := time.Duration(t.Trigger.EverySeconds) * time.Second; t.Trigger.Kind == schedule.KindInterval && every > 0 {
		due = due.Truncate(every)
	}
	ctx, cancel := context.WithTimeout(h.s.ctx, scheduleClaimTimeout)
	defer cancel()
	ok, err := claim(ctx, t.ID, due.UTC().Format(time.RFC3339))
	if err != nil {
		h.s.log().Debug("schedules: asking for a run failed", "schedule", t.ID, "err", err)
		return mine
	}
	return ok
}

// fillRepositoryKey works out what a host checks out for a schedule, from its folder's remote.
func fillRepositoryKey(t *schedule.Task) {
	if t.AwayHostID == "" || t.RepositoryKey != "" || t.Cwd == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), scheduleClaimTimeout)
	defer cancel()
	if repo, ok := vcs.ParseRemote(workspace.GitRemote(ctx, t.Cwd)); ok {
		t.RepositoryKey = repo.Key
	}
}

// hostCheckout makes the checkout of a run on an always-on host: a schedule with a repository runs there from a fresh
// branch. Elsewhere, or without a repository, the schedule's own folder stands.
func (h *scheduleHost) hostCheckout(t schedule.Task) (func(context.Context, string), string, error) {
	m := h.s.HostMode
	if m == nil || t.SessionID != "" {
		return nil, "", nil
	}
	if t.RepositoryKey == "" {
		if t.DeviceID != h.s.DeviceID {
			return nil, "", errors.New("this schedule names no repository to check out on the host")
		}
		return nil, "", nil
	}
	if t.Mode == "bypassPermissions" {
		return nil, "", errors.New("bypassPermissions cannot be used on an always-on host")
	}
	ctx, cancel := context.WithTimeout(h.s.ctx, 20*time.Minute)
	defer cancel()
	cwd, tracked, err := m.Prepare(ctx, t.RepositoryKey)
	if err != nil {
		return nil, "", err
	}
	return tracked, cwd, nil
}

// allowOnHost lets this host run what it was named for without "Allow on this machine": whoever can edit the document can
// start any session here anyway. bypassPermissions stays refused (hostCheckout).
func (h *scheduleHost) allowOnHost(st *schedule.State, id string) {
	m := h.s.HostMode
	rec := st.Records[id]
	if m == nil || rec == nil {
		return
	}
	named := rec.Task.DeviceID == h.s.DeviceID || (m.HostID != nil && rec.Task.AwayHostID != "" && rec.Task.AwayHostID == m.HostID())
	if named && rec.Task.DeviceID != "" {
		rec.Trusted = rec.Task.TrustHash()
	}
}
