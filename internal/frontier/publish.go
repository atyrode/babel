package frontier

import (
	"context"
	"database/sql"

	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// This file is the encode side of the wire form remote.go decodes: it turns a
// row this store has just written into a staged Phase B record on its way to
// the shared catalog (SPEC.md §6.5, §9, issue #109 item 1).
//
// It is a separate file from store.go because it is a separate concern with a
// separate failure mode. store.go's job is that a durable write is atomic,
// immutable and attributable on this machine; this file's job is that the same
// write becomes owed to the fleet in the same instant. The staging call lives
// inside store.go's existing transactions - it has to, and that is the whole
// point - but everything about what is staged and how it is shaped is here.
//
// The import is named babelsync deliberately. internal/sync's package name
// shadows the standard library's, and one vocabulary for the import name across
// the packages that consume it is worth more than the two characters.

// Option configures a Store at Open time.
//
// It is a variadic option rather than a parameter because every existing caller
// of Open predates Phase B publication and must keep compiling unchanged: a
// local-only deployment is a supported deployment, and the frontier works
// exactly as before without a hook.
type Option func(*Store)

// WithSync attaches the Phase B publication hook.
//
// Without it - and that is the default - the frontier is a purely local durable
// store: nothing is staged, nothing is published, and no write path behaves
// differently. With it, every durable record this store writes is staged inside
// the same transaction that writes it, so "durable here" and "owed to the
// fleet" become one event rather than two with a crash window between them.
func WithSync(h babelsync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// publication is what a write path learns inside its transaction and acts on
// after it: the closure to publish, and whether there is one to publish yet.
//
// There often is not, and that is correct rather than a miss. A record produced
// by a run whose closure is still open joins it and publishes nothing, because
// migration 0003 fixes a run's record_count at declaration and never lets it
// move - so a closure may not be declared while it can still grow. The run
// declares and publishes itself when it ends, and internal/explore is what
// knows when that is.
type publication struct {
	closure babelsync.Closure
	publish bool
}

// stage stages one record inside the caller's transaction.
//
// producedBy is the run that produced the record, and empty for an operator's
// own act. That distinction decides the closure, and it is not a detail: an
// operator's decision about a finding from a run that ended last week is not
// part of that run's output closure, and staging it into one would try to join
// a closure the run already declared. internal/sync's Append resolves it in one
// place so no write site has to.
//
// A nil hook makes this a no-op, which is what local-only mode is.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, producedBy string, rec babelsync.Record) (publication, error) {
	if s.sync == nil {
		return publication{}, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, producedBy, rec)
	if err != nil {
		return publication{}, err
	}
	return publication{closure: closure, publish: publish}, nil
}

// commit attempts to publish what stage staged, after the writer's transaction
// has committed.
//
// It is best-effort by contract. internal/sync returns nil for every transient
// failure - an unreachable catalog, a refused object write, a closure the
// catalog does not yet hold in full - and hands one diagnostic line to the
// command surface, because SPEC.md §6.5 makes publication a step that may be
// completed later and never a step a local write depends on. A returned error
// is a caller bug in this file, and a test must not swallow it.
func (s *Store) commit(ctx context.Context, p publication) error {
	if s.sync == nil || !p.publish {
		return nil
	}
	return s.sync.CommitInline(ctx, p.closure)
}

// staged builds the record internal/sync stages from a wire form and its
// identity. It exists so the six kinds share one marshalling step and one
// schema, and so a new kind cannot forget to validate on the way out: Marshal
// runs PublishedRecord.validate, which refuses a malformed record before it can
// become a content-addressed object nothing ever deletes.
//
// The plaintext subject projection (migrations/0010, #114) is derived here from
// the same wire field the sealed object carries, rather than passed in
// alongside it. That is the whole reason it is one function: the columns a
// keyless fleet reader sees and the object a key-holding one opens are two
// views of one list, so they cannot disagree about what a proposal rests on.
func staged(id string, kind sharedcatalog.RecordKind, wire PublishedRecord) (babelsync.Record, error) {
	payload, err := wire.Marshal()
	if err != nil {
		return babelsync.Record{}, err
	}
	var subjects []sharedcatalog.RecordSubject
	if len(wire.RestsOn) > 0 {
		subjects = make([]sharedcatalog.RecordSubject, 0, len(wire.RestsOn))
		for _, subject := range wire.RestsOn {
			subjects = append(subjects, sharedcatalog.RecordSubject{
				Kind: sharedcatalog.SubjectKind(subject.Kind),
				ID:   subject.ID,
			})
		}
	}
	return babelsync.Record{
		EntityID: id,
		Kind:     kind,
		Schema:   RecordSchema,
		Payload:  payload,
		Subjects: subjects,
	}, nil
}

// The six builders below are the mapping from this package's records to
// migration 0003's closed kind vocabulary. Five map exactly. The sixth is
// stated rather than assumed: a refinement request is not a decision, but 0003
// admits `disposition` for "internal/frontier's records and their append-only
// review material", and §4.7 is explicit that a refinement exists only because
// a recorded rejection authorized it - so it travels as the review material of
// that rejection. Widening the vocabulary would be a migration and a review,
// and is deliberately not taken here.

func stagedHypothesis(record Hypothesis, rootID string, payload []byte) (babelsync.Record, error) {
	return staged(record.ID, sharedcatalog.KindHypothesis, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedHypothesis,
		ID:        record.ID,
		RootID:    rootID,
		Ancestor:  record.AncestorID,
		RunID:     record.RunID,
		Status:    record.Status,
		CreatedAt: record.CreatedAt,
		Payload:   payload,
	})
}

func stagedObservation(record Observation, rootID string, payload []byte) (babelsync.Record, error) {
	return staged(record.ID, sharedcatalog.KindObservation, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedObservation,
		ID:        record.ID,
		RootID:    rootID,
		Ancestor:  record.AncestorID,
		RunID:     record.RunID,
		CreatedAt: record.CreatedAt,
		Payload:   payload,
	})
}

func stagedFinding(record Finding, rootID string, payload []byte) (babelsync.Record, error) {
	return staged(record.ID, sharedcatalog.KindFinding, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedFinding,
		ID:        record.ID,
		RootID:    rootID,
		Ancestor:  record.AncestorID,
		RunID:     record.RunID,
		CreatedAt: record.CreatedAt,
		Payload:   payload,
	})
}

// stagedProposal carries what the proposal rests on in RestsOn, because #114
// gives a proposal two forms and nothing else on the wire tells them apart.
//
// Which ids go there depends on the form and the difference is not cosmetic. A
// consolidated proposal rests on its findings, and its hypotheses are a
// derivation through those findings' observations - publishing the derivation
// would claim the proposal rests on claims it only reaches. A candidate
// proposal rests on exactly the hypotheses it addresses, which for that form
// are all HypothesisIDs holds, because the transitive half contributes nothing
// when there is no finding to travel through.
func stagedProposal(record Proposal, rootID string, payload []byte) (babelsync.Record, error) {
	return staged(record.ID, sharedcatalog.KindProposal, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedProposal,
		ID:        record.ID,
		RootID:    rootID,
		Ancestor:  record.AncestorID,
		RunID:     record.RunID,
		CreatedAt: record.CreatedAt,
		RestsOn:   proposalRestsOn(record),
		Payload:   payload,
	})
}

// proposalRestsOn names the records one proposal rests on, in the order its
// own rows hold them.
func proposalRestsOn(record Proposal) []PublishedSubject {
	ids, kind := record.FindingIDs, EntityFinding
	if record.Form == ProposalCandidate {
		ids, kind = record.HypothesisIDs, EntityHypothesis
	}
	subjects := make([]PublishedSubject, 0, len(ids))
	for _, id := range ids {
		subjects = append(subjects, PublishedSubject{Kind: kind, ID: id})
	}
	return subjects
}

// stagedLink carries the endpoints in PublishedEdge because a link's whole
// meaning is outside its payload: LinkPayload holds a note and nothing else, so
// a reader given only the note and 0003's plaintext row would know that
// something was asserted about the corpus and not what.
//
// A link has no revision chain, so it is its own root and has no ancestor.
func stagedLink(record Link, payload []byte) (babelsync.Record, error) {
	return staged(record.ID, sharedcatalog.KindLink, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedLink,
		ID:        record.ID,
		RootID:    record.ID,
		CreatedAt: record.CreatedAt,
		Edge: &PublishedEdge{
			FromID: record.FromID,
			ToID:   record.ToID,
			Type:   record.Type,
		},
		Payload: payload,
	})
}

// stagedDisposition carries the decision and the reviewer in PublishedAnswer
// for the same reason stagedLink carries endpoints: neither is in the payload
// locally - both are columns of frontier_disposition, and the payload holds
// only the note - so restating them keeps the wire form a faithful copy of the
// row rather than a reshaped one.
func stagedDisposition(event DispositionEvent, payload []byte) (babelsync.Record, error) {
	return staged(event.ID, sharedcatalog.KindDisposition, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedReviewAnswer,
		ID:        event.ID,
		RootID:    event.ID,
		Subject:   event.Subject,
		Answer:    &PublishedAnswer{Decision: event.Disposition, Reviewer: event.ReviewerID},
		CreatedAt: event.RecordedAt,
		Payload:   payload,
	})
}

// stagedRefinement is the second form a review answer takes. Its Answer
// carries no decision, and that absence is the discriminator rather than an
// omission: §4.7 states there is no standalone `refine` disposition, so an
// empty decision says "this is the request a recorded rejection authorized".
func stagedRefinement(request RefinementRequest, reviewer string, payload []byte) (babelsync.Record, error) {
	return staged(request.ID, sharedcatalog.KindDisposition, PublishedRecord{
		Schema:    RecordSchema,
		Kind:      PublishedReviewAnswer,
		ID:        request.ID,
		RootID:    request.ID,
		Subject:   request.Subject,
		Answer:    &PublishedAnswer{Reviewer: reviewer},
		CreatedAt: request.CreatedAt,
		Payload:   payload,
	})
}
