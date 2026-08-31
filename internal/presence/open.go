package presence

import (
	"context"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is the one place a presence store is assembled from configuration,
// and it is separate from store.go so that everything else in this package
// stays testable against an injected connection: New takes a pool and knows
// nothing about documents.

// ErrNotConfigured reports that this machine has no fleet to be present in.
var ErrNotConfigured = errors.New("presence: no shared catalog is configured")

// NotConfigured reports whether an error means there is no fleet, as opposed to
// a fleet that could not be reached or a document that is wrong.
//
// It exists so a surface has one predicate to branch on, the same way
// fleet.NotConfigured does. A view that meets this says the machine is in local
// mode; a view that meets anything else has met a real failure - use
// Unreachable to tell an unreachable PostgreSQL from a broken document - and
// must say so rather than degrading into an empty list that looks like an
// answer.
func NotConfigured(err error) bool { return errors.Is(err, ErrNotConfigured) }

// Open assembles a presence store from this machine's storage configuration.
// The returned store owns its connection; the caller owes Close.
//
// It needs no payload keys, and that is load-bearing rather than incidental
// (#112). A presence row is identifiers, closed vocabularies and timestamps
// with no sealed payload anywhere in it, so a host that holds the catalog
// credential and no key at all can read the whole fleet's presence. Requiring a
// keyring here - as internal/fleet's reader legitimately does, because every
// record it lists would otherwise be unopenable - would make "what is running
// where" a question only key-holding machines could ask, for no reason but a
// shared constructor.
func Open(ctx context.Context, cfg config.Config, localHostID string, diag func(error)) (*Store, error) {
	if cfg.Mode != config.ModeShared || cfg.Catalog == nil {
		return nil, fmt.Errorf("%w: this machine runs in local mode", ErrNotConfigured)
	}
	if cfg.DeploymentID == "" {
		return nil, fmt.Errorf("%w: the storage configuration names no deployment", ErrNotConfigured)
	}
	if localHostID == "" {
		return nil, fmt.Errorf("%w: this machine's own host identity is unresolved", ErrNotConfigured)
	}

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
	if err != nil {
		// Returned as it arrived rather than flattened into ErrNotConfigured:
		// a configured deployment whose PostgreSQL is down is not a
		// configuration answer, and sharedcatalog.Unreachable is what tells
		// the two apart.
		return nil, err
	}
	store, err := New(Options{
		DB:           db,
		DeploymentID: cfg.DeploymentID,
		HostID:       localHostID,
		Diag:         diag,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	store.owned = db
	return store, nil
}
