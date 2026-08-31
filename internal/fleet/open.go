package fleet

import (
	"context"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is the one place a fleet reader is assembled from configuration,
// and it is separate from fleet.go so that everything else in this package
// stays testable with injected fakes: NewReader takes four dependencies and
// knows nothing about documents, and OpenReader is the only function here that
// reads one.

// OpenReader assembles the fleet read surface from this machine's storage
// configuration: the shared catalog connection, the Phase B object store, and
// the payload keyring.
//
// Three things can be absent, and they are three different answers rather than
// one failure:
//
//   - Local mode. There is no fleet, and saying so is the correct answer to
//     "show me every host's records". ErrNotConfigured.
//   - Shared mode with no payload keys. The catalog is reachable and every
//     plaintext row is readable, but no record's content can be opened. That
//     is still ErrNotConfigured, because a reader that could list records and
//     never read one would make every listing a wall of unopenable rows - and
//     the message names the key document, which is the thing to fix.
//   - A catalog that cannot be reached. That is not a configuration answer at
//     all: the deployment is configured and PostgreSQL is down, and
//     sharedcatalog.Unreachable is what tells those apart, so the error is
//     returned as it arrived rather than flattened into ErrNotConfigured.
//
// The caller owns the returned reader's connection lifetime through Close.
func OpenReader(ctx context.Context, cfg config.Config, localHostID string) (*Reader, error) {
	if cfg.Mode != config.ModeShared || cfg.Catalog == nil {
		return nil, fmt.Errorf("%w: this machine runs in local mode", ErrNotConfigured)
	}
	if cfg.DeploymentID == "" {
		return nil, fmt.Errorf("%w: the storage configuration names no deployment", ErrNotConfigured)
	}

	keys, found, err := config.LoadPayloadKeys()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: no payload keys at %s, so no record's content can be opened",
			ErrNotConfigured, config.PayloadKeysPath())
	}
	active, material, err := keys.Material()
	if err != nil {
		return nil, err
	}
	ring, err := envelope.RingFrom(envelope.KeyID(active), keyIDs(material))
	if err != nil {
		return nil, err
	}

	store, err := objectstore.Open(cfg)
	if err != nil {
		return nil, err
	}

	// The catalog is opened last, because it is the only step that touches the
	// network: a misconfigured key document or an unreadable repository locator
	// should be reported without dialling PostgreSQL first.
	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN())
	if err != nil {
		return nil, err
	}
	reader, err := NewReader(db, store, ring, cfg.DeploymentID, localHostID)
	if err != nil {
		db.Close()
		return nil, err
	}
	reader.owned = db
	return reader, nil
}

// keyIDs converts the key material config hands back into the map envelope
// wants.
//
// It is three lines in the one place that needs them, deliberately. Neither
// internal/config nor internal/envelope is the right home: config would have to
// import envelope for a type it otherwise never mentions, and envelope's own
// package comment promises it holds no storage integration, which a function
// shaped around a document's return type would quietly break.
func keyIDs(material map[string][]byte) map[envelope.KeyID][]byte {
	out := make(map[envelope.KeyID][]byte, len(material))
	for id, key := range material {
		out[envelope.KeyID(id)] = key
	}
	return out
}

// Close releases the catalog connection, if this reader opened one.
//
// A reader built by NewReader borrows its connection and closes nothing: the
// caller that opened it owns it, and closing a caller's pool from underneath it
// is how a long-lived process loses its catalog. Only OpenReader's own
// connection is owned here, so Close is safe to call on either.
func (r *Reader) Close() error {
	if r == nil || r.owned == nil {
		return nil
	}
	db := r.owned
	r.owned = nil
	if err := db.Close(); err != nil {
		return fmt.Errorf("close catalog connection: %w", err)
	}
	return nil
}

// NotConfigured reports whether an error means there is no fleet to read, as
// opposed to a fleet that could not be reached or a document that is wrong.
//
// It exists so a rendering surface has one predicate to branch on. A listing
// that meets this shows what it has locally and says why there is nothing else;
// a listing that meets anything else has met a real failure and must say so
// rather than degrading into an empty page that looks like an answer.
func NotConfigured(err error) bool { return errors.Is(err, ErrNotConfigured) }
