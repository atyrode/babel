package reality

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// This file is the wire form of one Reality Ledger record between hosts, and
// the staging step that puts it there (SPEC.md §6.5, §9, issue #109 item 2).
//
// It is a separate file from store_*.go for the same reason internal/frontier's
// is: those files' job is that a durable write is atomic, immutable and
// attributable on this machine, and this file's job is that the same write
// becomes owed to the fleet in the same instant. The staging call lives inside
// their existing transactions — it has to, and that is the whole point — but
// what is staged and how it is shaped is here.
//
// SPEC.md §266 makes globally durable encrypted Reality Ledger state a Phase B
// capability, and this is the half of it this package owns. Until it ran, a
// fact an operator answered for existed on exactly one disk, the durable
// database is not under the hourly archive roots, and losing that machine lost
// answers no re-index recovers.
//
// The import is named babelsync deliberately. internal/sync's package name
// shadows the standard library's, and one vocabulary for the import name across
// the packages that consume it is worth more than the two characters.

// catalogKind is the shared catalog record kind every Reality record publishes
// under.
//
// The vocabulary is closed. migrations/0003 holds a database CHECK over exactly
// nine values — hypothesis, observation, finding, proposal, link, disposition,
// context, preparation, receipt — and none of them is a Reality kind. Nothing
// this package writes can widen it.
//
// `context` is the nearest of the nine and it is chosen deliberately rather
// than exactly. 0003 documents it as attributed operator guidance; SPEC.md §4.7
// says operator context is attributed guidance and never independent evidence,
// and decision 30 says only operator actions and predicate-scoped trusted
// imports authorize facts. Everything this package publishes is attributed
// operator material on those terms: a fact an operator answered for, the answer
// they gave, the guidance they attached, the subject those speak about, the
// identity decisions and contradictions they judged, the acceptance that put an
// interpretation of their answer under their authority, and the batch of a
// source they registered. This is not a claim that a Reality fact *is* operator
// guidance. It is the closest thing the closed vocabulary can say about one.
//
// What the mapping cannot say, the envelope does. PublishedRecord.Kind carries
// the record's own type inside the sealed payload, so a reader recovers which of
// the kinds below it is holding even though the plaintext row can only say
// `context`. Nothing is lost; it is moved behind the key, which is where §9
// wants everything that is not on its allowlist anyway.
//
// Naming Reality's records in the catalog would be a PostgreSQL migration and a
// review of what a new plaintext kind reveals about a deployment. That is
// deliberately not taken here.
const catalogKind = sharedcatalog.KindContext

// PublishedKind names a Reality record type on the wire.
//
// It exists because migration 0003's row cannot say it. That row carries an id,
// a kind, a schema version, an ordinal and a reference to the sealed object,
// and every record below publishes under the one catalog kind above — so
// without this discriminator a reader holding the decrypted object would have
// to infer what it was looking at from the shape of the payload, which is
// guessing.
type PublishedKind string

// The Reality records this package publishes. Which write paths produce them,
// and which durable writes deliberately produce none, is stated on stage.
const (
	// PublishedEntity is one stable subject. It travels because every other
	// envelope names one: a fact whose subject the reader does not hold is a
	// claim about nothing.
	PublishedEntity PublishedKind = "entity"
	// PublishedFact is one immutable fact revision — §4.8's central record,
	// and the one an operator answered for.
	PublishedFact PublishedKind = "fact"
	// PublishedAnswer is one operator answer, retained verbatim. §4.8
	// requires the retention and no other machine can produce the text.
	PublishedAnswer PublishedKind = "answer"
	// PublishedContext is one piece of attributed operator guidance (§4.7).
	PublishedContext PublishedKind = "context"
	// PublishedImport is one trusted-source batch: the row that authorizes
	// the facts committed with it — decision 30 admits operator actions and
	// predicate-scoped trusted imports and nothing else — and the fact count
	// the source declared, which is what lets a reading host tell a whole
	// inventory batch from half of one.
	PublishedImport PublishedKind = "import"
	// PublishedResolution is one entry of the alias merge/split history
	// SPEC.md §266 names among Phase B's globally durable state. It travels
	// because §4.8 requires a mistaken resolution to stay reversible, and a
	// reversal needs the resolution it reverses to be addressable.
	PublishedResolution PublishedKind = "resolution"
	// PublishedMembership is one move in an identity's append-only
	// resolution history: what it now resolves to, and which resolution said
	// so. Without these a published resolution tells a second machine that
	// two identities merged without saying into what.
	PublishedMembership PublishedKind = "membership"
	// PublishedDispute is one contradiction a human or an accepted plan
	// judged. Only those travel; the ones the deterministic check opens by
	// itself are derived, and stage says why.
	PublishedDispute PublishedKind = "dispute"
	// PublishedPlan is one interpretation of an operator's answer, with the
	// actions it proposed. It travels because an acceptance names it, and an
	// acceptance whose plan the reader does not hold is an authority over
	// nothing.
	PublishedPlan PublishedKind = "plan"
	// PublishedAcceptance is one operator acceptance of a plan — §4.8's
	// authority behind every fact, resolution and dispute the acceptance
	// applied, and the record those commit under.
	PublishedAcceptance PublishedKind = "acceptance"
)

func (k PublishedKind) valid() bool {
	switch k {
	case PublishedEntity, PublishedFact, PublishedAnswer, PublishedContext,
		PublishedImport, PublishedResolution, PublishedMembership,
		PublishedDispute, PublishedPlan, PublishedAcceptance:
		return true
	}
	return false
}

// carriesPayload reports whether this kind's row has a payload_json column for
// the envelope to carry.
//
// Two of them do not. reality_import and reality_entity_membership are
// allowlisted columns and nothing else — a batch is a source, a key, an instant
// and a count; a membership entry is an identity, a role, a canonical pointer
// and the resolution that wrote it — so their envelope is entirely structure.
// Composing a payload for them out of those same columns would be a second
// rendering of what the envelope already carries, which is the derived-field
// mistake PublishedRecord refuses everywhere else.
func (k PublishedKind) carriesPayload() bool {
	switch k {
	case PublishedImport, PublishedMembership:
		return false
	}
	return true
}

// PublishedRecord is the canonical plaintext form of one Reality record on its
// way through the shared catalog: what a reader cannot recover from the
// plaintext row, plus the stored payload bytes unchanged.
//
// What must be here is decided by what 0003 refuses to store. That row gives
// PostgreSQL an id, a kind, a schema version, an ordinal and the sealed-object
// reference and deliberately no more, because every additional plaintext column
// is a fact a managed provider can read without a key. A Reality record's own
// type, its subject entity, its valid time and its authority are none of those
// columns, and all four are load-bearing: without the type a reader cannot tell
// an answer from a fact, without the subject a claim is about nothing, without
// the valid time a superseded fact and a current one are indistinguishable, and
// without the authority §4.8's rule that only operator actions and trusted
// imports authorize facts cannot be checked on the reading host at all.
//
// What must not be here is anything derived. A fact's expiry is its observation
// time plus its predicate's TTL, and its value kind and object entity are
// inside the payload; a reader computes each of those from what it already
// holds. A derived field on the wire is a second answer to a question that
// already has one, and two answers can disagree across versions.
//
// What is also not here is any lifecycle state, and that is a stated limit
// rather than an oversight. A fact's status, an alias's retraction and a
// question's disposition are append-only histories that keep arriving after the
// record is durable, while 0003 makes a published record insert-only by
// trigger. Freezing a status into one would publish a claim that becomes false
// the moment the next status event lands, so the envelope carries the immutable
// revision and never its current standing.
//
// Payload is the record's stored payload_json bytes. Nothing re-encodes
// operator prose on the way out or on the way in.
type PublishedRecord struct {
	// Schema is the ledger record shape's own version (RecordSchema),
	// independent of the shared catalog's. A reader that meets a version it
	// does not understand says so rather than decoding optimistically.
	Schema int           `json:"schema"`
	Kind   PublishedKind `json:"kind"`
	ID     string        `json:"id"`
	// RecordedAt is when this machine's ledger made the record durable. It
	// is not when the recorded thing happened, which is what Claim's valid
	// time and AuthoredAt are for, and conflating the two is how a fact
	// recorded today about last month reads as a fact about today.
	RecordedAt time.Time `json:"recorded_at"`
	// EntityKind types an entity's subject and is empty for every other
	// kind. It is one field rather than a sub-object because an entity's
	// whole plaintext half is its kind: everything else about it — the
	// display name §9 will not put in the clear — is in the payload.
	//
	// The entity's role and canonical identity are deliberately absent. They
	// are not properties of the row but the newest entry of an append-only
	// membership history that a later merge moves. A new entity resolves to
	// itself, and every later move travels as its own membership entry under
	// the resolution that made it, so freezing one in here would publish a
	// claim that goes stale the moment a merge lands.
	EntityKind EntityKind `json:"entity_kind,omitempty"`
	// Claim carries a fact's subject, valid time and attribution, and is nil
	// for every other kind.
	Claim *PublishedClaim `json:"claim,omitempty"`
	// Response carries what an operator's answer answers and what it amounts
	// to, and is nil for every other kind.
	Response *PublishedResponse `json:"response,omitempty"`
	// Batch carries an import's source, key and declared count, and is nil
	// for every other kind.
	Batch *PublishedBatch `json:"batch,omitempty"`
	// Identity carries what one resolution did to a set of identities, and
	// Membership one identity's move under it. They are two kinds rather
	// than one because they are two rows with two lifetimes: the resolution
	// is written once and never appended to, while the membership history it
	// wrote into goes on growing with every later resolution.
	Identity   *PublishedIdentity        `json:"identity,omitempty"`
	Membership *PublishedMembershipEntry `json:"membership,omitempty"`
	// Contradiction carries what a dispute is about, and is nil for every
	// other kind.
	Contradiction *PublishedContradiction `json:"contradiction,omitempty"`
	// Interpretation carries a plan and the actions it proposed; Approval
	// carries the acceptance that put one under an operator's authority.
	Interpretation *PublishedInterpretation `json:"interpretation,omitempty"`
	Approval       *PublishedApproval       `json:"approval,omitempty"`
	// Author is the operator identity that acted, and AuthoredAt the instant
	// they supplied the material. Which kinds carry which is decided by
	// which rows have those columns, and validate spells the two rules out:
	// an operator act with no actor is not attributable, and a record with a
	// supplied-at instant its row never stored would be inventing one.
	Author     string    `json:"author,omitempty"`
	AuthoredAt time.Time `json:"authored_at,omitzero"`
	// Payload is the record's sealed content: the operator's own words, the
	// claim's value, the provenance locator. It is the one field that is not
	// structural, and it is why the whole envelope is sealed rather than
	// partly plaintext. It is absent for the two kinds whose row has no
	// payload column at all; see carriesPayload.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PublishedClaim carries what a fact says, about what, over what time, and on
// whose authority.
//
// None of it is in the payload because none of it is in the payload locally
// either: subject, predicate, valid time, observation time, authority,
// confidence and sensitivity are all columns of reality_fact, and the payload
// holds the value, the provenance locator and the reasoning. Restating them
// here keeps the wire form a faithful copy of the row rather than a reshaped
// one.
//
// The authority is three fields rather than the Authority struct because a wire
// format's field names are a contract and that struct has no JSON tags: it has
// never travelled, and giving it some here would decide its wire shape for
// every future encoder of it from inside this file.
type PublishedClaim struct {
	SubjectID string    `json:"subject_id"`
	Predicate Predicate `json:"predicate"`
	// ValidFrom and ValidUntil are the fact's own valid time. An absent
	// ValidUntil is open-ended, which is the common case for operator
	// intent — and it is absent rather than a zero instant, because a reader
	// comparing the zero time against a range would find every intent fact
	// long expired.
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until,omitzero"`
	// ObservedAt is when the authority observed what it asserts, which is
	// what §4.8's freshness is measured from. The derived expiry is not
	// carried: a reader adds the predicate's TTL itself.
	ObservedAt time.Time `json:"observed_at"`
	// AuthorityKind, AuthorityID and AuthorityAt are §4.8's attribution: who
	// stands behind the fact and when they said so. All three or the record
	// is refused — an attribution missing its identity or its instant is not
	// an attribution, and the reading host has nothing else to check
	// decision 30's rule against.
	AuthorityKind AuthorityKind `json:"authority_kind"`
	AuthorityID   string        `json:"authority_id"`
	AuthorityAt   time.Time     `json:"authority_at"`
	Confidence    Confidence    `json:"confidence"`
	// Sensitivity travels because a reader that does not know a fact is
	// restricted cannot honour it, and §9 grades disclosure risk precisely so
	// that it does not have to be guessed from the content.
	Sensitivity Sensitivity `json:"sensitivity"`
	// Supersedes is the revision this one replaces, empty for an original.
	// It is the revision order: a reading host holding two revisions of one
	// claim has nothing else to order them by except clocks it does not
	// control.
	Supersedes string `json:"supersedes,omitempty"`
}

// PublishedResponse carries an answer's lineage and its outcome.
//
// Outcome is here rather than inferred from the text because §4.8 singles out
// `unknown` and `declined` as outcomes that suppress equivalent re-asks: it is
// a stored vocabulary value, not a reading of prose, and a host that re-derived
// it from the words would be interpreting an answer it was told to retain
// verbatim.
type PublishedResponse struct {
	// QuestionID is the question this answers. It is lineage rather than a
	// promise: a question's subject matter is a digest of its own canonical
	// targets and predicates, so a second machine holding this ledger derives
	// the equivalent question for itself under a different id. Questions are
	// not published — see stage — so this may name a record the reading host
	// does not hold.
	QuestionID string        `json:"question_id"`
	Outcome    AnswerOutcome `json:"outcome"`
	// ContextID links attributed guidance supplied with the answer. It is a
	// link and never support: §4.7 makes Context guidance rather than
	// evidence.
	ContextID string `json:"context_id,omitempty"`
}

// PublishedBatch carries what one trusted-source import was: whose inventory it
// came from, which batch of theirs it was, and how many facts it declared.
//
// The count is the source's own claim about the batch, restated from the row,
// and not the same number as the closure's record count — that one is Babel's
// count of what it staged and includes this record. Half an inventory batch is
// worse than none, and this is what lets a reading host say it is looking at
// half rather than assume that whatever arrived was all there was.
//
// The batch key is the source's own deduplication key and stays opaque to
// Babel: ImportFacts refuses a replayed one rather than importing it twice.
type PublishedBatch struct {
	SourceID  string `json:"source_id"`
	BatchKey  string `json:"batch_key"`
	FactCount int    `json:"fact_count"`
}

// PublishedIdentity carries what one resolution did: which operation, which
// identities it consumed, which it produced, and — for an undo — which earlier
// resolution it reverses.
//
// The member lists are here rather than in the payload because that is where
// they are locally. reality_resolution_member is written by the same statement
// sequence as the resolution row and nothing ever appends to it again, so it is
// the resolution's own content. The membership entries the resolution appended
// are the history, and they travel as their own records.
type PublishedIdentity struct {
	Kind ResolutionKind `json:"kind"`
	// ReversesID is the resolution this one undoes, empty unless Kind is an
	// undo. §4.8's history keeps a mistake beside its correction, and this
	// is the link that pairs them on a host holding both.
	ReversesID string `json:"reverses_id,omitempty"`
	// SourceIDs are the identities the resolution consumed — a merge's
	// sources, a split's parent, an undo's subject — and ResultIDs the ones
	// it produced or preserved.
	SourceIDs []string `json:"source_ids"`
	ResultIDs []string `json:"result_ids"`
}

// PublishedMembershipEntry carries one move in an identity's append-only
// resolution history: what it is now, what speaks for it, and which resolution
// said so.
//
// The row's sequence number is deliberately absent. It is a per-identity local
// counter, so two hosts' third entry for one identity are different entries and
// a reader ordering by it would be ordering by a number that means nothing off
// the host that wrote it. The recorded instant on the envelope is the order.
type PublishedMembershipEntry struct {
	EntityID string     `json:"entity_id"`
	Role     EntityRole `json:"role"`
	// CanonicalID is the identity that now speaks for this one. It is the
	// answer to the question a resolution alone cannot answer: two
	// identities merged, into what.
	CanonicalID string `json:"canonical_id"`
	// ResolutionID is the resolution that wrote this entry. It is always
	// set, because the one entry that has none is an entity's first — written
	// by its own creation to say it speaks for itself — and that one is not
	// published.
	ResolutionID string `json:"resolution_id"`
}

// PublishedContradiction carries what a dispute is about: the subject and
// predicate two or more facts disagree on, and which facts they are.
//
// The dispute's state is absent for the reason PublishedRecord gives — it is an
// append-only history that keeps arriving — so a reading host learns that these
// facts were judged to conflict and never that the conflict is still open. The
// survivor is named by ResolveDispute, whose whole output is that history.
type PublishedContradiction struct {
	SubjectID string    `json:"subject_id"`
	Predicate Predicate `json:"predicate"`
	FactIDs   []string  `json:"fact_ids"`
}

// PublishedInterpretation carries one plan: the answer it interpreted, the
// interpreter that produced it, and the actions it proposed.
//
// The actions ride inside the plan rather than travelling as records of their
// own, and that is a judgement about what a plan action is. It has no reader of
// its own — a plan is read whole — and its state column is the single mutable
// thing in this schema, because it records what Babel did with the row rather
// than what the row says. So the immutable half is the plan's content and
// travels with it, and the state stays home like every other lifecycle.
type PublishedInterpretation struct {
	QuestionID string `json:"question_id"`
	AnswerID   string `json:"answer_id"`
	// InterpreterVersion is which interpreter produced the plan. §4.8 wants
	// interpretation versioned, so a reader comparing two plans for one
	// answer is comparing two interpreters rather than two moods.
	InterpreterVersion int               `json:"interpreter_version"`
	Actions            []PublishedAction `json:"actions"`
}

// PublishedAction is one action a plan proposed, with its stored payload bytes
// unchanged.
//
// Its state, its result and the instant it was applied are absent: those are
// what an acceptance later did with it, and the acceptance travels as its own
// record in the closure that applied it.
type PublishedAction struct {
	ID string `json:"id"`
	// Position is the action's place in the plan, which is the order the
	// operator reviewed it in before accepting.
	Position int             `json:"position"`
	Kind     ActionKind      `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

// PublishedApproval is the acceptance row's own half: which plan the operator
// accepted, and the guidance they attached to accepting it.
//
// §4.8 makes the accepting operator the authority behind everything the
// acceptance applied, which is why every fact, resolution, membership entry and
// dispute of one acceptance commits in the acceptance's closure.
type PublishedApproval struct {
	PlanID string `json:"plan_id"`
	// ContextID links attributed guidance supplied with the acceptance. It
	// is a link and never support: §4.7 makes Context guidance rather than
	// evidence.
	ContextID string `json:"context_id,omitempty"`
}

// validate refuses a wire record that could not be read back as the record it
// claims to be. It is the one validation both directions share: Marshal calls
// it so a malformed record never leaves the machine, and DecodePublishedRecord
// calls it so a record that arrived from another machine is checked with the
// rules that let it out of that one.
//
// Every rule is refused in both directions, and the second direction is the one
// worth spelling out. Ten record types share one envelope, so nothing but this
// stops a piece of guidance from shipping a subject entity, an entity from
// shipping a valid time, or a membership entry from shipping a claim. A reader
// would then have to decide whether to believe such a field, and a wire format
// whose fields are sometimes meaningful is a wire format with no contract at
// all.
func (p PublishedRecord) validate() error {
	if p.ID == "" {
		return fmt.Errorf("%w: published record carries no id", ErrInvalidValue)
	}
	if p.Schema < 1 {
		return fmt.Errorf("%w: published record %s carries no schema version", ErrInvalidValue, p.ID)
	}
	if p.Schema > RecordSchema {
		return fmt.Errorf("%w: published record %s is schema %d and this build reads %d",
			ErrInvalidValue, p.ID, p.Schema, RecordSchema)
	}
	if !p.Kind.valid() {
		return fmt.Errorf("%w: record %s is kind %q", ErrInvalidValue, p.ID, p.Kind)
	}
	if p.RecordedAt.IsZero() {
		return fmt.Errorf("%w: published record %s does not say when it was recorded",
			ErrInvalidValue, p.ID)
	}
	// A JSON null is not a payload, and the length check alone does not catch
	// it: a Payload field that is present and null is four bytes. A record
	// with no content at all would otherwise travel in both directions and
	// surface much later as a fact whose value is empty rather than as the
	// malformed record it is.
	payload := bytes.TrimSpace(p.Payload)
	if bytes.Equal(payload, []byte("null")) {
		payload = nil
	}
	switch {
	case p.Kind.carriesPayload() && len(payload) == 0:
		return fmt.Errorf("%w: published record %s carries no payload", ErrInvalidValue, p.ID)
	case !p.Kind.carriesPayload() && len(payload) != 0:
		return fmt.Errorf("%w: a %s record carries no payload, because its row has no payload column",
			ErrInvalidValue, p.Kind)
	}

	// Each row below names the half exactly one kind carries. A record
	// missing its own half cannot be read as what it claims to be; a record
	// carrying another kind's half can be read as two things, which is worse.
	for _, part := range []struct {
		kind    PublishedKind
		present bool
		noun    string
	}{
		{PublishedEntity, p.EntityKind != "", "entity kind"},
		{PublishedFact, p.Claim != nil, "claim"},
		{PublishedAnswer, p.Response != nil, "answer"},
		{PublishedImport, p.Batch != nil, "import batch"},
		{PublishedResolution, p.Identity != nil, "resolution"},
		{PublishedMembership, p.Membership != nil, "membership entry"},
		{PublishedDispute, p.Contradiction != nil, "contradiction"},
		{PublishedPlan, p.Interpretation != nil, "interpretation"},
		{PublishedAcceptance, p.Approval != nil, "accepted plan"},
	} {
		switch {
		case p.Kind == part.kind && !part.present:
			return fmt.Errorf("%w: %s %s carries no %s", ErrInvalidValue, p.Kind, p.ID, part.noun)
		case p.Kind != part.kind && part.present:
			return fmt.Errorf("%w: a %s record carries no %s", ErrInvalidValue, p.Kind, part.noun)
		}
	}

	if p.EntityKind != "" && !p.EntityKind.valid() {
		return fmt.Errorf("%w: entity %s kind %q", ErrInvalidValue, p.ID, p.EntityKind)
	}
	if err := p.Claim.validate(p.ID); err != nil {
		return err
	}
	if err := p.Response.validate(p.ID); err != nil {
		return err
	}
	if err := p.Batch.validate(p.ID); err != nil {
		return err
	}
	if err := p.Identity.validate(p.ID); err != nil {
		return err
	}
	if err := p.Membership.validate(p.ID); err != nil {
		return err
	}
	if err := p.Contradiction.validate(p.ID); err != nil {
		return err
	}
	if err := p.Interpretation.validate(p.ID); err != nil {
		return err
	}
	if err := p.Approval.validate(p.ID); err != nil {
		return err
	}

	// Attribution is two questions rather than one, and the schema answers
	// them separately.
	//
	// Who acted is a column of five of these rows: an answer's author, a
	// piece of guidance's author, a resolution's actor, an acceptance's
	// actor, and — the one taken from the transaction rather than the row —
	// the operator a dispute was opened by, because reality_dispute has no
	// actor column and the judgement is on the state event the same
	// transaction appends. §4.8 cannot treat an unattributed operator act as
	// authority, so an absent actor is a refusal rather than a blank field.
	//
	// The other five carry their attribution elsewhere or nowhere: a fact's
	// is in its claim, an import's is the source in its batch, a membership
	// entry's is the resolution that wrote it, an entity is written down on
	// an operator's behalf, and a plan is an interpreter's output — §4.8
	// forbids agent interpretation from silently becoming authoritative
	// reality, so a plan naming an operator would be claiming exactly that.
	//
	// When they said it is a separate column on exactly two rows,
	// reality_answer's answered_at and reality_context's supplied_at, and
	// those are the two records an operator hands to Babel after the fact.
	// Every other act here is recorded as it happens, so its instant is
	// RecordedAt and a second one would be the same value twice.
	acted := p.Kind == PublishedAnswer || p.Kind == PublishedContext ||
		p.Kind == PublishedResolution || p.Kind == PublishedAcceptance ||
		p.Kind == PublishedDispute
	supplied := p.Kind == PublishedAnswer || p.Kind == PublishedContext
	switch {
	case acted && p.Author == "":
		return fmt.Errorf("%w: %s %s names no operator", ErrInvalidValue, p.Kind, p.ID)
	case !acted && p.Author != "":
		return fmt.Errorf("%w: a %s record carries no operator attribution", ErrInvalidValue, p.Kind)
	case supplied && p.AuthoredAt.IsZero():
		return fmt.Errorf("%w: %s %s does not say when the operator supplied it",
			ErrInvalidValue, p.Kind, p.ID)
	case !supplied && !p.AuthoredAt.IsZero():
		return fmt.Errorf("%w: a %s record carries no supplied-at instant", ErrInvalidValue, p.Kind)
	}
	return nil
}

// The validators below check one half against the record it claims to be. A nil
// half is nothing to check: whether one is permitted at all is decided by the
// table in PublishedRecord.validate, and repeating that decision here would be
// two answers to one question.

// validate checks a claim against the fact it claims to be.
func (c *PublishedClaim) validate(id string) error {
	if c == nil {
		return nil
	}
	if c.SubjectID == "" {
		return fmt.Errorf("%w: fact %s names no subject", ErrInvalidValue, id)
	}
	if !c.Predicate.valid() {
		return fmt.Errorf("%w: fact %s predicate %q", ErrInvalidValue, id, c.Predicate)
	}
	if c.ValidFrom.IsZero() {
		return fmt.Errorf("%w: fact %s says nothing about when it holds", ErrInvalidValue, id)
	}
	if c.ObservedAt.IsZero() {
		return fmt.Errorf("%w: fact %s says nothing about when it was observed", ErrInvalidValue, id)
	}
	if !c.AuthorityKind.valid() {
		return fmt.Errorf("%w: fact %s authority kind %q", ErrInvalidValue, id, c.AuthorityKind)
	}
	if c.AuthorityID == "" || c.AuthorityAt.IsZero() {
		return fmt.Errorf("%w: fact %s attribution names no identity or no instant", ErrInvalidValue, id)
	}
	if !c.Confidence.valid() {
		return fmt.Errorf("%w: fact %s confidence %q", ErrInvalidValue, id, c.Confidence)
	}
	if !c.Sensitivity.valid() {
		return fmt.Errorf("%w: fact %s sensitivity %q", ErrInvalidValue, id, c.Sensitivity)
	}
	return nil
}

func (r *PublishedResponse) validate(id string) error {
	if r == nil {
		return nil
	}
	if r.QuestionID == "" {
		return fmt.Errorf("%w: answer %s names no question", ErrInvalidValue, id)
	}
	if !r.Outcome.valid() {
		return fmt.Errorf("%w: answer %s outcome %q", ErrInvalidValue, id, r.Outcome)
	}
	return nil
}

// validate checks a batch against the import it claims to be. A count below one
// is refused because ImportFacts refuses an empty batch: a row saying an
// inventory ran and asserted nothing is not a record of anything.
func (b *PublishedBatch) validate(id string) error {
	if b == nil {
		return nil
	}
	if b.SourceID == "" {
		return fmt.Errorf("%w: import %s names no trusted source", ErrInvalidValue, id)
	}
	if b.BatchKey == "" {
		return fmt.Errorf("%w: import %s names no batch key", ErrInvalidValue, id)
	}
	if b.FactCount < 1 {
		return fmt.Errorf("%w: import %s declares %d facts", ErrInvalidValue, id, b.FactCount)
	}
	return nil
}

// validate checks one identity operation.
//
// The reversal link is refused in both directions: only an undo reverses
// anything, and an undo naming nothing would be a reversal with no mistake
// beside it, which is the pairing §4.8's append-only history exists to keep.
// Both member lists are required because every operation has both sides — a
// merge folds sources into a target, a split turns a parent into parts, an undo
// turns one back into the other — so an empty side is a resolution that did
// nothing to somebody.
func (i *PublishedIdentity) validate(id string) error {
	if i == nil {
		return nil
	}
	if !i.Kind.valid() {
		return fmt.Errorf("%w: resolution %s kind %q", ErrInvalidValue, id, i.Kind)
	}
	switch {
	case i.Kind == ResolutionUndo && i.ReversesID == "":
		return fmt.Errorf("%w: undo %s names no resolution to reverse", ErrInvalidValue, id)
	case i.Kind != ResolutionUndo && i.ReversesID != "":
		return fmt.Errorf("%w: a %s resolution reverses nothing", ErrInvalidValue, i.Kind)
	}
	if len(i.SourceIDs) == 0 || len(i.ResultIDs) == 0 {
		return fmt.Errorf("%w: resolution %s names no identity on one of its two sides",
			ErrInvalidValue, id)
	}
	return nil
}

// validate checks one membership entry. An entry with no identity is a move of
// nothing, and one with no canonical pointer is the very claim the entry exists
// to carry left out.
func (m *PublishedMembershipEntry) validate(id string) error {
	if m == nil {
		return nil
	}
	if m.EntityID == "" {
		return fmt.Errorf("%w: membership entry %s names no identity", ErrInvalidValue, id)
	}
	if !m.Role.valid() {
		return fmt.Errorf("%w: membership entry %s role %q", ErrInvalidValue, id, m.Role)
	}
	if m.CanonicalID == "" {
		return fmt.Errorf("%w: membership entry %s does not say what %s now resolves to",
			ErrInvalidValue, id, m.EntityID)
	}
	if m.ResolutionID == "" {
		return fmt.Errorf("%w: membership entry %s names no resolution", ErrInvalidValue, id)
	}
	return nil
}

// validate checks one contradiction. Two facts is the floor because one fact
// contradicts nothing, which is DisputeFacts' own rule.
func (c *PublishedContradiction) validate(id string) error {
	if c == nil {
		return nil
	}
	if c.SubjectID == "" {
		return fmt.Errorf("%w: dispute %s names no subject", ErrInvalidValue, id)
	}
	if !c.Predicate.valid() {
		return fmt.Errorf("%w: dispute %s predicate %q", ErrInvalidValue, id, c.Predicate)
	}
	if len(c.FactIDs) < 2 {
		return fmt.Errorf("%w: dispute %s names %d facts, and one fact contradicts nothing",
			ErrInvalidValue, id, len(c.FactIDs))
	}
	return nil
}

// validate checks one interpretation.
//
// Each action's position is checked against its place in the list because they
// are the same ordering twice: reality_plan_action's position is the order the
// operator reviewed the plan in, and a list that disagreed with its own
// positions would leave a reader to pick one of the two.
func (i *PublishedInterpretation) validate(id string) error {
	if i == nil {
		return nil
	}
	if i.QuestionID == "" || i.AnswerID == "" {
		return fmt.Errorf("%w: plan %s does not name both a question and an answer",
			ErrInvalidValue, id)
	}
	if i.InterpreterVersion < 1 {
		return fmt.Errorf("%w: plan %s names no interpreter version", ErrInvalidValue, id)
	}
	if len(i.Actions) == 0 {
		return fmt.Errorf("%w: plan %s proposes nothing, and a plan has at least one action, "+
			"which may be no-action", ErrInvalidValue, id)
	}
	for position, action := range i.Actions {
		if action.ID == "" {
			return fmt.Errorf("%w: plan %s action %d has no id", ErrInvalidValue, id, position)
		}
		if action.Position != position {
			return fmt.Errorf("%w: plan %s action %s is %d records in and says it is position %d",
				ErrInvalidValue, id, action.ID, position, action.Position)
		}
		if !action.Kind.valid() {
			return fmt.Errorf("%w: plan %s action %s kind %q", ErrInvalidValue, id, action.ID, action.Kind)
		}
		body := bytes.TrimSpace(action.Payload)
		if len(body) == 0 || bytes.Equal(body, []byte("null")) {
			return fmt.Errorf("%w: plan %s action %s carries no payload, so it states no rationale",
				ErrInvalidValue, id, action.ID)
		}
	}
	return nil
}

// validate checks one accepted plan.
func (a *PublishedApproval) validate(id string) error {
	if a == nil {
		return nil
	}
	if a.PlanID == "" {
		return fmt.Errorf("%w: acceptance %s names no plan", ErrInvalidValue, id)
	}
	return nil
}

// Marshal encodes the record for sealing.
//
// It is a method rather than a bare json.Marshal at the call site so that the
// bytes a publisher seals and the bytes an ingest decodes are produced and
// consumed by one pair of functions, and so the validation runs on the way out.
// A record refused here never becomes an object, which is the cheap place to
// catch it: an object is content-addressed and never deleted, and 0003 makes
// the row that names it insert-only, so a malformed one is permanent.
func (p PublishedRecord) Marshal() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("reality: encode published record %s: %w", p.ID, err)
	}
	return out, nil
}

// DecodePublishedRecord reads the wire form back out of decrypted plaintext.
//
// It validates rather than trusting, because these bytes arrived from another
// machine. They are authenticated — the envelope's associated data binds them to
// the record's global id and kind, so a swapped object fails to open at all —
// but authentication proves origin, not shape: a record written by a build
// whose payload shape has moved on, or one carrying a kind this build has no
// surface for, is authentic and still not decodable here. Reporting that as a
// refusal with the id in it is how an operator learns to look at versions
// rather than at storage.
func DecodePublishedRecord(plaintext []byte) (PublishedRecord, error) {
	var p PublishedRecord
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return PublishedRecord{}, fmt.Errorf("reality: decode published record: %w", err)
	}
	if err := p.validate(); err != nil {
		return PublishedRecord{}, err
	}
	return p, nil
}

// publication is what a write path learns inside its transaction and acts on
// after it: the closure to publish, and whether there is one to publish yet.
type publication struct {
	closure babelsync.Closure
	publish bool
}

// stage stages one record inside the caller's transaction, as its own closure
// of one.
//
// Staging shares the writer's transaction rather than following it, and that is
// the whole reason it happens here rather than after the commit: a record that
// committed locally while its journal row did not would be durable, invisible
// to the publisher, and reported by nothing — which for this file means an
// answer an operator gave that no other machine will ever see and nothing
// saying so.
//
// There is no producing run, and the parameter internal/frontier passes is
// absent here rather than always empty because there is no case where it could
// be anything else. Nobody resumes an operator's action: no run will later end
// and declare a closure on the record's behalf, so a Reality record that joined
// an open closure would wait for a declaration that never comes. internal/sync
// reads the empty run as "this record is its own closure of one" and declares
// it inside this same transaction, so a crash between the commit and the
// publication attempt leaves a declared closure the next `babel sync` finishes.
//
// Seven write paths call this, and they are the ones whose whole publishable
// output is one record no other machine can reproduce: CreateEntity,
// AssertFact, SupersedeFact, RecordAnswer, AttachContext, DisputeFacts and
// RecordPlan.
//
// Five others commit a *set* that has to reach the fleet together or not at
// all, and they use stageSet, which is where that reasoning lives: ImportFacts,
// MergeEntities, SplitEntity, UndoResolution and AcceptPlan.
//
// The rest of this package's durable writes publish nothing, and the reason is
// per path rather than one rule:
//
//   - A question. Babel derives it: its subject matter is a digest of its own
//     canonical target entities and predicates, so a second machine holding
//     this ledger raises the equivalent question itself. The operator's answer
//     is the thing no other machine can produce, which is why the answer
//     travels and the prompt does not.
//   - Append-only lifecycle events — a fact's status, an alias or relationship
//     retraction, a question's state, a dispute's state. They keep arriving
//     after the record is durable, and 0003's rows are insert-only, so
//     freezing one into a published record would publish a claim that becomes
//     false when the next event lands. ResolveDispute is nothing but these: it
//     appends one status event per member fact and one dispute state event, so
//     it has no record of its own to publish and the survivor it named is
//     visible only on the owning host.
//   - A dispute the contradiction check opened by itself. `contradictions`
//     derives it — same canonical subject, same predicate, overlapping valid
//     time, different value — from facts that travel on their own, so a reading
//     host computes it exactly as it computes a question. A dispute a human or
//     an accepted plan judged is derivable from nothing, and that one travels.
//   - A trusted source registration. It is configuration Babel is told rather
//     than something it learned: every machine running the same dotfiles
//     registers the same source under the same id with the same declared
//     scope, so a lost registration costs a re-registration and no facts.
//     Its id is also the one identifier here this package does not
//     generate — §4.8 requires the same inventory to be the same source across
//     machines and reinstalls, so an operator supplies it — and keying a staged
//     record on it would make a registration that succeeds locally fail on a
//     shared deployment whenever the configured id is not a well-formed Phase
//     B identifier.
//   - A focus rule set version. Its identity is a small integer chosen
//     locally, so two hosts' version 2 are different policies with the same
//     wire id and 0003's insert-only row would make whichever arrived first
//     permanent. An accepted plan's change-focus action therefore stages
//     nothing for the rule set it installed.
//   - A context snapshot. It is the one durable thing in this package that is
//     derived rather than recorded: `resolve` and `evaluateFocus` compute every
//     row of it from facts, identities and a rule set version that all travel
//     or are configuration, so what a lost snapshot costs is a re-derivation
//     and never a fact. It also names a hypothesis internal/frontier owns and a
//     rule set by that local integer version, so publishing it would put two
//     identities on the wire that mean nothing on the receiving host.
//   - A plan's non-authoritative descendants. A retained hypothesis is
//     internal/frontier's record and internal/frontier publishes it, so
//     staging it here would republish another component's row under a Reality
//     id; a follow-up question is a question; and a recorded pipeline request
//     is a work item for the local review pipeline rather than a claim about
//     reality, which §4.6 and decision 13 keep outside this package entirely.
//
// A nil hook makes all of this a no-op, which is what local-only mode is.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, rec babelsync.Record) (publication, error) {
	if s.sync == nil {
		return publication{}, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, "", rec)
	if err != nil {
		return publication{}, err
	}
	return publication{closure: closure, publish: publish}, nil
}

// recordSet collects the wire records one write path must publish together.
//
// A set publishes as one closure of several rather than as several closures of
// one, and that is the atomicity §4.8 asks of these operations carried across a
// boundary this store cannot reach on its own. Locally the transaction makes
// the whole set exist together or not at all. Remotely, migration 0003 makes
// the run row the visibility boundary and flips it to committed only when the
// catalog holds the whole declared closure — so independent closures would let
// a fleet reader see half a set: an acceptance without the facts it authorized,
// a merge without the membership entries that say what the merged identity now
// resolves to, half an inventory batch. There is no repair for that on the read
// side, which is why the boundary is the closure and not the record.
//
// The set does not choose its own anchor, and that is deliberate: the same
// merge is anchored on its own resolution when an operator asked for it and on
// the acceptance when a plan did, so the record that authorizes a set is known
// to the write path and not to the helper. stageSet takes it.
//
// A nil *recordSet is what a local-only store gets, and add tolerates it, so a
// write path needs no branch for local mode beyond the one it would write
// anyway. A path whose set grows with its input tests for nil before encoding
// anything — ImportFacts does — because a deployment with no shared backend must
// not pay to encode five hundred records it will never stage.
type recordSet struct {
	records []babelsync.Record
}

// newRecordSet starts a set, or returns nil when there is no backend to publish
// to.
func (s *Store) newRecordSet() *recordSet {
	if s.sync == nil {
		return nil
	}
	return &recordSet{}
}

// add takes a builder's two results directly, so a call site is one line and a
// build failure cannot be mistaken for a staged record.
func (set *recordSet) add(rec babelsync.Record, err error) error {
	if set == nil {
		return nil
	}
	if err != nil {
		return err
	}
	set.records = append(set.records, rec)
	return nil
}

// stageSet stages the whole set inside the caller's transaction, anchored on
// the record that authorizes it, and closes its closure there too.
//
// The anchor is the record without which the others must not exist, and it is
// the closure's run id. Run ids and record ids live in different PostgreSQL
// tables, so reusing a record's id as its closure's run id collides with
// nothing and says exactly what the row means: this commit is this set. Every
// caller stages the anchor's own record into the set, so a declared closure is
// never empty.
//
// The declaration shares the transaction for the same reason the staging does:
// 0003 fixes a run's record_count at declaration, so declaring inside the
// writer's transaction is what makes "the fleet sees this set or none of it" a
// database property instead of a convention. A crash between the commit and the
// publication attempt then leaves a declared closure the next `babel sync`
// finishes, rather than a set staged with nobody to declare it.
//
// A staging failure is returned, and the caller's transaction rolls the durable
// write back with it.
func (s *Store) stageSet(ctx context.Context, tx *sql.Tx, anchorID string,
	set *recordSet) (publication, error) {
	if set == nil {
		return publication{}, nil
	}
	for _, rec := range set.records {
		rec.RunID = anchorID
		if err := s.sync.StageTx(ctx, tx, rec); err != nil {
			return publication{}, err
		}
	}
	closure := babelsync.Closure{RunID: anchorID}
	if err := s.sync.DeclareTx(ctx, tx, closure); err != nil {
		return publication{}, err
	}
	return publication{closure: closure, publish: true}, nil
}

// commit attempts to publish what stage staged, after the writer's transaction
// has committed.
//
// It is best-effort by contract. internal/sync returns nil for every transient
// failure — an unreachable catalog, a refused object write, a missing
// credential — and hands one diagnostic line to the command surface, because
// SPEC.md §6.5 makes publication a step that may be completed later and never a
// step a local write depends on. The record stays durable and visibly
// pending-sync and the operator's command still succeeds.
//
// A returned error is a caller bug in this file, and a test must not swallow
// it.
func (s *Store) commit(ctx context.Context, p publication) error {
	if s.sync == nil || !p.publish {
		return nil
	}
	return s.sync.CommitInline(ctx, p.closure)
}

// staged builds the record internal/sync stages from a wire form and its
// identity. It exists so every kind shares one marshalling step and one
// schema, and so a new kind cannot forget to validate on the way out: Marshal
// runs PublishedRecord.validate, which refuses a malformed record before it can
// become an object nothing ever deletes.
func staged(id string, wire PublishedRecord) (babelsync.Record, error) {
	payload, err := wire.Marshal()
	if err != nil {
		return babelsync.Record{}, err
	}
	return babelsync.Record{
		EntityID: id,
		Kind:     catalogKind,
		Schema:   RecordSchema,
		Payload:  payload,
	}, nil
}

// The builders below turn a row this store has just written into its wire
// form.
//
// Each encodes the payload from the same value, through the same function, that
// the insert encoded the row's payload_json from, so the envelope carries the
// row's own bytes rather than a second rendering of them. Reading the column
// back inside the transaction would cost a query per write to learn what the
// caller is still holding.
//
// Four of the rows have no schema_version column of their own — a resolution, a
// membership entry, an import batch and an acceptance — and those stamp
// RecordSchema, which is by definition the version this build writes.

func stagedEntity(record Entity) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     record.SchemaVersion,
		Kind:       PublishedEntity,
		ID:         record.ID,
		RecordedAt: record.CreatedAt,
		EntityKind: record.Kind,
		Payload:    payload,
	})
}

func stagedFact(record Fact) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     record.SchemaVersion,
		Kind:       PublishedFact,
		ID:         record.ID,
		RecordedAt: record.RecordedAt,
		Claim: &PublishedClaim{
			SubjectID:     record.SubjectID,
			Predicate:     record.Predicate,
			ValidFrom:     record.ValidFrom,
			ValidUntil:    record.ValidUntil,
			ObservedAt:    record.ObservedAt,
			AuthorityKind: record.Authority.Kind,
			AuthorityID:   record.Authority.ID,
			AuthorityAt:   record.Authority.At,
			Confidence:    record.Confidence,
			Sensitivity:   record.Sensitivity,
			Supersedes:    record.Supersedes,
		},
		Payload: payload,
	})
}

func stagedAnswer(record Answer) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     record.SchemaVersion,
		Kind:       PublishedAnswer,
		ID:         record.ID,
		RecordedAt: record.RecordedAt,
		Response: &PublishedResponse{
			QuestionID: record.QuestionID,
			Outcome:    record.Outcome,
			ContextID:  record.ContextID,
		},
		Author:     record.Author,
		AuthoredAt: record.At,
		Payload:    payload,
	})
}

// stagedContext takes the instant the ledger recorded the guidance because
// Context is the one record whose struct does not carry it: a reader of
// guidance wants to know when the operator supplied it, so the type exposes
// that and the row keeps the rest.
//
// Its schema version is RecordSchema for the same reason: reality_context has
// no schema column, and RecordSchema is by definition the version this build
// stamps on everything it writes.
func stagedContext(record Context, recordedAt time.Time) (babelsync.Record, error) {
	payload, err := marshalPayload(ContextPayload{Text: record.Text})
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedContext,
		ID:         record.ID,
		RecordedAt: recordedAt,
		Author:     record.Author,
		AuthoredAt: record.At,
		Payload:    payload,
	})
}

// stagedImport takes the batch row's columns rather than a record type because
// the ledger has none: reality_import is written inline by ImportFacts and read
// back through the facts that name it, so there is nothing to pass but this.
func stagedImport(id, sourceID, batchKey string, factCount int,
	importedAt time.Time) (babelsync.Record, error) {
	return staged(id, PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedImport,
		ID:         id,
		RecordedAt: importedAt,
		Batch: &PublishedBatch{
			SourceID:  sourceID,
			BatchKey:  batchKey,
			FactCount: factCount,
		},
	})
}

func stagedResolution(record Resolution) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedResolution,
		ID:         record.ID,
		RecordedAt: record.RecordedAt,
		Identity: &PublishedIdentity{
			Kind:       record.Kind,
			ReversesID: record.ReversesID,
			SourceIDs:  record.SourceIDs,
			ResultIDs:  record.ResultIDs,
		},
		Author:  record.Actor,
		Payload: payload,
	})
}

// stagedMembership names the record by the resolution that wrote the entry and
// the identity it moved.
//
// The row's own key is (entity_id, seq) and cannot be the wire identity: seq is
// a per-identity local counter, so two hosts' third entry for one identity are
// different entries. Both halves of this pair are global instead, and one
// resolution moves each identity exactly once — a merge moves each source, a
// split its parent, an undo the identities the original touched — so the pair is
// unique and says what the record is.
func stagedMembership(entry membership) (babelsync.Record, error) {
	id := entry.resolutionID + "." + entry.entityID
	return staged(id, PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedMembership,
		ID:         id,
		RecordedAt: entry.recordedAt,
		Membership: &PublishedMembershipEntry{
			EntityID:     entry.entityID,
			Role:         entry.role,
			CanonicalID:  entry.canonicalID,
			ResolutionID: entry.resolutionID,
		},
	})
}

// stagedDispute takes the actor separately because reality_dispute has no actor
// column: the operator who judged the contradiction is on the state event the
// same transaction appends, and an unattributed operator judgement is not one
// §4.8 can weigh.
func stagedDispute(record Dispute, actor string) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     record.SchemaVersion,
		Kind:       PublishedDispute,
		ID:         record.ID,
		RecordedAt: record.CreatedAt,
		Contradiction: &PublishedContradiction{
			SubjectID: record.SubjectID,
			Predicate: record.Predicate,
			FactIDs:   record.FactIDs,
		},
		Author:  actor,
		Payload: payload,
	})
}

func stagedPlan(record Plan) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	actions := make([]PublishedAction, 0, len(record.Actions))
	for _, action := range record.Actions {
		encoded, err := marshalPayload(action.Payload)
		if err != nil {
			return babelsync.Record{}, err
		}
		actions = append(actions, PublishedAction{
			ID:       action.ID,
			Position: action.Position,
			Kind:     action.Kind,
			Payload:  encoded,
		})
	}
	return staged(record.ID, PublishedRecord{
		Schema:     record.SchemaVersion,
		Kind:       PublishedPlan,
		ID:         record.ID,
		RecordedAt: record.CreatedAt,
		Interpretation: &PublishedInterpretation{
			QuestionID:         record.QuestionID,
			AnswerID:           record.AnswerID,
			InterpreterVersion: record.InterpreterVersion,
			Actions:            actions,
		},
		Payload: payload,
	})
}

func stagedAcceptance(record Acceptance) (babelsync.Record, error) {
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return babelsync.Record{}, err
	}
	return staged(record.ID, PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedAcceptance,
		ID:         record.ID,
		RecordedAt: record.RecordedAt,
		Approval: &PublishedApproval{
			PlanID:    record.PlanID,
			ContextID: record.ContextID,
		},
		Author:  record.Actor,
		Payload: payload,
	})
}
