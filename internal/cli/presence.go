package cli

import (
	"context"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is where a command acquires the fleet-presence announcer of #118.
// It is one function because there is one rule: a command that is about to run
// analysis takes whatever presence it can get and proceeds regardless.

// openPresence opens this machine's presence store, or returns nil when there
// is none to open.
//
// Nil is the ordinary answer and never a failure. A machine in local mode has
// no fleet to be present in; a machine whose PostgreSQL is unreachable has one
// and cannot reach it; and in both cases the run must happen anyway, because
// presence is status about analysis and analysis does not wait on status. So
// every reason not to have a store is stated once on the diagnostic stream and
// the command carries on with a nil announcer, which internal/explore and
// internal/conductor both document as the feature quietly absent.
//
// The two reasons are still told apart in what is printed. "This machine is in
// local mode" is a fact about the deployment and needs no action; "the shared
// catalog could not be reached" is a fault, and an operator who sees an empty
// fleet view wants to know which of the two they are looking at.
//
// The returned closer is always non-nil, so a caller defers it unconditionally.
//
// The announcer is returned as the interface rather than as *presence.Store,
// and that is the point of the signature: a nil *presence.Store placed in an
// Announcer is a non-nil interface holding a nil pointer, which would pass
// every `Presence == nil` check downstream and then quietly do nothing. Failing
// here returns a literal nil, so "absent" is absent all the way down. The read
// half is opened separately by the surfaces that render the fleet; a command
// that runs analysis needs only the write half and takes only that.
func (a *app) openPresence(ctx context.Context) (presence.Announcer, func()) {
	cfg, _, err := config.Load()
	if err != nil {
		a.diagf("babel: presence: the stored configuration would not load, so this run is invisible to the fleet: %s\n",
			Sanitize(err.Error()))
		return nil, func() {}
	}
	if cfg.Mode != config.ModeShared {
		// Not worth a line: local mode is a deployment choice, and a machine
		// that has never configured a fleet does not need to be told on every
		// run that it has none.
		return nil, func() {}
	}
	host, err := localHostID()
	if err != nil {
		a.diagf("babel: presence: this machine's own identity is unresolved, so this run is invisible to the fleet: %s\n",
			Sanitize(err.Error()))
		return nil, func() {}
	}

	store, err := presence.Open(ctx, cfg, host, a.presenceDiag)
	switch {
	case err == nil:
	case presence.NotConfigured(err):
		a.diagf("babel: presence: %s\n", Sanitize(err.Error()))
		return nil, func() {}
	case sharedcatalog.Unreachable(err):
		a.diagf("babel: presence: the shared catalog could not be reached, so this run is invisible to the fleet: %s\n",
			Sanitize(err.Error()))
		return nil, func() {}
	default:
		a.diagf("babel: presence: %s; this run is invisible to the fleet\n", Sanitize(err.Error()))
		return nil, func() {}
	}
	return store, func() { store.Close() }
}

// presenceDiag is where a failed presence write lands. Every one of them is a
// note about visibility and never about the run: the run committed what it
// committed, and the only consequence is that another machine cannot see it
// happening.
func (a *app) presenceDiag(err error) {
	a.diagf("babel: presence: %s\n", Sanitize(err.Error()))
}
