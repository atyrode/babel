package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/atyrode/babel/internal/sharedcatalog"
	// internal/sync, not the standard library's: what this file needs from a
	// package by that name is the Phase B publication hook, not a mutex.
	"github.com/atyrode/babel/internal/sync"
)

// This file is the whole of this package's coupling to Phase B publication:
// what each record's canonical publication bytes are, which closure it
// belongs to, and the rule that a failed publication is never a failed write.
//
// It is separate from store.go because those are three decisions rather than
// three lines of plumbing, and a reader asking "what do we send, and when"
// should not have to read the SQL to find out (SPEC.md §6.5, §9, §12 Phase B).

// remoteReceipt is a run receipt's canonical publication form.
//
// It exists because the durable row's payload column holds the body alone.
// The receipt's identity, the run it records, the preparation it ran over,
// its place in the revision chain, when it was recorded, under whose
// authority and what it counted all live in sibling columns, so a reader on
// another host handed the stored payload could not say which run those bytes
// belonged to or which revision they were. Publishing the header beside the
// body is what makes the sealed object self-describing.
//
// Nothing here is derived. The body travels as the exact bytes this machine
// stored rather than re-encoded, and the preparation the header names is
// published as its own record rather than copied in: several runs share one
// corpus scope, and a second copy of it is a second thing that can disagree.
type remoteReceipt struct {
	Header Header          `json:"header"`
	Body   json.RawMessage `json:"body"`
}

// newRemoteReceipt builds the publication form of a stored receipt from its
// header and the exact payload bytes the durable row holds.
//
// The local sync state is cleared rather than carried. It is not a property
// of the record but this machine's knowledge of where the record has reached;
// it is `pending-sync` by construction at the moment of staging, since that
// is what the row was just written as; and the catalog's own run state is
// what answers the question for a reader. Shipping it would publish a second
// answer that is wrong from the moment it arrives.
func newRemoteReceipt(h Header, body []byte) remoteReceipt {
	h.Sync = ""
	return remoteReceipt{Header: h, Body: body}
}

// validate refuses a publication payload that would be useless on arrival.
//
// A record reaches the shared catalog once and is immutable there (migration
// 0003), so a payload missing the identity or the lineage a reader needs
// cannot be corrected afterwards - it can only be superseded by another
// record. Refusing it here fails the local write instead, which is where a
// caller bug is still fixable.
func (r remoteReceipt) validate() error {
	switch {
	case r.Header.ID == "":
		return fmt.Errorf("run: a published receipt carries its own id")
	case r.Header.RunID == "":
		return fmt.Errorf("run: a published receipt carries the run it records")
	case r.Header.PreparationID == "":
		return fmt.Errorf("run: a published receipt carries the preparation it ran over")
	case r.Header.Revision < 1:
		return fmt.Errorf("run: a published receipt carries its revision")
	case r.Header.Sync != "":
		return fmt.Errorf("run: a published receipt carries no local sync state")
	case len(r.Body) == 0:
		return fmt.Errorf("run: a published receipt carries its body")
	}
	return nil
}

// MarshalJSON renders the publication bytes, validating first so that no path
// through encoding/json can produce a payload validate would have refused.
func (r remoteReceipt) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	// A local defined type sheds the method set, so encoding the value here
	// does not call this method again.
	type publication remoteReceipt
	b, err := json.Marshal(publication(r))
	if err != nil {
		return nil, fmt.Errorf("run: encode receipt publication: %w", err)
	}
	return b, nil
}

// stagePreparation stages a preparation inside the transaction that is making
// it durable, and reports the closure to publish once that transaction has
// committed.
//
// producedBy is empty because a preparation is its own closure of one:
// `babel prepare` fixes a corpus scope before any run exists, so no run
// produced it and none could still grow its closure. Append therefore
// declares the closure in this same transaction, and no crash can leave the
// record staged with nobody to declare it.
//
// The stored bytes are the publication bytes. MarshalCanonical renders the
// whole record - schema, content-derived identity, the time the scope was
// fixed, the selection and the prior outputs it named - so a reader on
// another host needs nothing the row does not already hold, and re-encoding
// it here would only be a chance to disagree with what was written.
func (s *Store) stagePreparation(ctx context.Context, tx *sql.Tx,
	p Preparation, payload []byte) (sync.Closure, bool, error) {
	if s.sync == nil {
		return sync.Closure{}, false, nil
	}
	return s.sync.Append(ctx, tx, "", sync.Record{
		EntityID: string(p.ID),
		Kind:     sharedcatalog.KindPreparation,
		Schema:   p.Schema,
		Payload:  payload,
	})
}

// stageReceipt stages a receipt inside the transaction that is making it
// durable, and reports the closure to publish once that transaction has
// committed.
//
// producedBy is the run the receipt records, so in the normal case the
// receipt joins that run's closure and nothing publishes here: a run's
// closure may not be declared while it can still grow, and internal/explore
// declares and publishes the whole run when the run ends. A later amendment,
// written after that closure was already declared, cannot join it, and Append
// makes it its own closure of one linked to the run instead - which is the
// honest shape, since an amendment is not part of a closed run's output but
// attached to it. There is no branch for the two cases here because Append is
// the one place that rule is allowed to live.
//
// The publication payload is built only when there is a hook to stage it for.
// It is a re-encoding of the whole receipt, and a local-only store must not
// pay for one on every write.
func (s *Store) stageReceipt(ctx context.Context, tx *sql.Tx,
	h Header, body []byte) (sync.Closure, bool, error) {
	if s.sync == nil {
		return sync.Closure{}, false, nil
	}
	payload, err := json.Marshal(newRemoteReceipt(h, body))
	if err != nil {
		return sync.Closure{}, false, err
	}
	return s.sync.Append(ctx, tx, h.RunID, sync.Record{
		EntityID: string(h.ID),
		Kind:     sharedcatalog.KindReceipt,
		Schema:   h.Schema,
		Payload:  payload,
	})
}

// publish attempts one declared closure's publication, immediately after the
// transaction that declared it committed.
//
// ready is Append's answer to whether the closure is complete; a closure that
// is not is left to whatever will declare it, and publishing early would fix
// a record_count the run has not reached. Publication itself is best-effort
// by contract: CommitInline hands every transient failure - an unreachable
// database, a refused object write, a missing credential, a closure the
// catalog does not yet hold in full - to its own diagnostic sink and returns
// nil, leaving the record durable and visibly pending-sync and the command
// that wrote it successful (SPEC.md §6.5).
//
// A returned error is therefore never an outage. It is this package having
// used the hook wrongly - a malformed record, a closure declared twice at two
// sizes - and the record is already durable by the time it surfaces, so
// reporting it is how such a bug reaches a test instead of becoming a row
// that stays pending forever with no remedy.
func (s *Store) publish(ctx context.Context, c sync.Closure, ready bool) error {
	if !ready {
		return nil
	}
	return s.sync.CommitInline(ctx, c)
}
