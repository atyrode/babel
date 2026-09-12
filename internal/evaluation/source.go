package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is evaluation's read adapter over everything a deployment has
// already produced: this machine's durable frontier, the Reality ledger's
// recorded work and allowances, and every other host's committed records.
//
// It exists because evaluation has no corpus of its own. The reviewable set is
// internal/frontier's four analysis kinds, the authority over them is
// internal/review's and internal/reality's recorded decisions, and the fleet's
// half of it arrives as sealed objects that only this machine's keyring can
// open. A second inventory of any of those would be a second answer to "what
// exists and what may be spent on it", so nothing here stores anything: it
// reads, decodes, and hands back values.
//
// Four properties hold throughout.
//
// Local artifacts do not wait for another host. docs/evaluation-lifecycle.md
// §E4 requires a never-reviewed observation to be found regardless of score or
// review-queue enrollment, and a deployment with no shared catalog at all is
// the normal single-machine case; so the local durable store is enumerated
// directly and the fleet read only adds what other machines published.
//
// Failure is per-record and named. A key this instance does not hold, a payload
// from a newer build, an object store that will not answer: each of those loses
// one record and must not lose the inventory — that is internal/fleet's rule
// and this package inherits it, because a coverage number that silently dropped
// what it could not open would report a gap as completeness. What it must not
// do is pretend: SourceStatus carries the reason, Coverage and Page surface it,
// and a shared mode that failed at startup is never quietly reported as local
// mode.
//
// Decoding is cached by digest. A refresh opens only the objects whose sealed
// digest it has not already decoded, with a bounded number of opens in flight.
// The alternative — reopening the deployment's whole committed history on every
// sweep — is the whole-corpus decrypt §E5 forbids, and it would grow without
// bound while the answer stayed the same.
//
// Nothing here infers a preference. Context is assembled from recorded facts,
// recorded questions and the versioned focus policy; a name that resolves to no
// entity, an ambiguous name and an uninstalled policy each become an explicit
// Context.Unknown entry rather than a default.

// FleetSource is the fleet read surface evaluation needs.
//
// It is an interface with two methods rather than *fleet.Reader because the
// dependency must point this way: internal/fleet decodes frontier records and
// reference edges and must not learn this package's vocabulary, so evaluation
// names what it needs and *fleet.Reader satisfies it structurally.
//
// Records lists without opening anything, and Open opens exactly one. The split
// is internal/fleet's and is load-bearing here: listing is one query and
// opening is one object fetch plus one decrypt, so the incremental cache below
// can list everything cheaply and open only what changed.
type FleetSource interface {
	Records(ctx context.Context, filter sharedcatalog.RecordFilter) ([]fleet.Record, error)
	Open(ctx context.Context, record sharedcatalog.FleetRecord) (fleet.Record, error)
}

// Source is everything the evaluation service reads.
//
// Artifacts is the reviewable inventory: the head revision of every covered
// artifact this deployment can see, local and remote. EvaluationRecords is the
// fleet's committed evaluation publications — other hosts' assessments,
// operator criteria and feedback, policies, and the assignment/attempt/
// checkpoint journal that lets a non-producing instance rebuild coverage rather
// than merely count votes. The embedded Resolver answers for one subject,
// including a superseded revision an assignment was bound to.
type Source interface {
	Resolver

	Artifacts(ctx context.Context) ([]Artifact, error)
	EvaluationRecords(ctx context.Context) ([]Record, error)
}

// RecordResolver looks one evaluation record up by id across the deployment.
//
// It is a separate optional interface because only one caller needs it and the
// need is narrow: an operator's criteria or reconsider decision may name a
// record another host published, and shape-checking the identifier is not the
// same as knowing it exists. The store consults this when it is wired and falls
// back to shape validation when it is not, so local mode stays legal.
type RecordResolver interface {
	EvaluationRecord(ctx context.Context, id string) (Record, error)
}

// SourceStatus reports how complete the last read was.
//
// It is read through StatusSource rather than returned from Artifacts because
// a partial read is still a successful read: the caller gets the artifacts it
// could see, and the degradation travels beside them into Coverage.Reason and
// Page.Unavailable. Returning an error instead would make one unopenable object
// on one remote host hide every artifact on this one.
type SourceStatus struct {
	// LocalOnly reports that no fleet source is wired, which is the
	// intentional single-machine configuration. It is deliberately distinct
	// from Unavailable: a shared mode that failed at startup must never be
	// reported as local mode, so the caller decides which of the two it
	// constructed and this only repeats it.
	LocalOnly bool
	// Unavailable is why the last read was incomplete, empty when it was
	// complete.
	Unavailable string
	// Unopened counts records this instance could list and not open.
	Unopened int
	// Unattributed counts records whose producing host the catalog could not
	// name. They are still read; the count exists so a coverage figure can
	// say that some of it is unattributed rather than implying every record
	// is placed.
	Unattributed int
	// ReadAt is when the last read finished.
	ReadAt time.Time
}

// StatusSource is the optional interface a Source implements when it can report
// how complete its last read was.
type StatusSource interface {
	Status() SourceStatus
}

// KindInventory is one produced record kind and what evaluation does with it.
//
// The inventory lists kinds evaluation does *not* review as well as the ones it
// does, and that is the point of the type. §E1 requires `not applicable` to be
// an intentional named policy with a reason and forbids a missing evaluator
// from reading as reviewed; a coverage page that simply omitted run receipts
// would satisfy neither, because an operator could not tell whether receipts
// were reviewed, exempt, or forgotten.
//
// LocalCounted and FleetCounted are separate from the counts because zero and
// "nothing here can count this" are different facts. A build with no adapter
// for a kind says so instead of reporting none of them.
type KindInventory struct {
	Kind       string `json:"kind"`
	Reviewable bool   `json:"reviewable"`
	// Reason is the named policy sentence for a kind evaluation does not
	// review, empty for a reviewable kind.
	Reason       string `json:"reason,omitempty"`
	Local        int    `json:"local"`
	LocalCounted bool   `json:"local_counted"`
	Fleet        int    `json:"fleet"`
	FleetCounted bool   `json:"fleet_counted"`
}

// Inventory is the optional interface a Source implements when it can enumerate
// the produced record kinds beyond the reviewable ones.
type Inventory interface {
	Produced(ctx context.Context) ([]KindInventory, error)
}

// maxConcurrentOpens bounds how many sealed objects are opened at once.
//
// Eight, against two costs that pull in opposite directions: an open is one
// network round trip to the object store plus one AEAD decrypt, so serial opens
// make a first refresh on a large deployment latency-bound, while unbounded
// fan-out would open every committed record in the deployment simultaneously
// and put the whole decrypted corpus in memory at once. Eight keeps the store
// busy without holding more than a handful of plaintexts.
const maxConcurrentOpens = 8

// fleetPageSize is how many catalog rows one listing call fetches. It is
// internal/sharedcatalog's own ceiling: the loop below runs to exhaustion, so a
// smaller page would only mean more round trips for the same rows.
const fleetPageSize = sharedcatalog.MaxRecordLimit

// SourceOption configures a Source.
//
// The options exist because the inventory and the recorded-pain signal need
// stores this package must not reach for itself. Each one is a borrowed handle
// the caller already holds open, and each is optional: a Source with none of
// them reports honest absence rather than a zero.
type SourceOption func(*babelSource)

// WithComplaints lends the operator-complaint store.
//
// It earns its place twice. A complaint is the only durable record of what the
// operator has actually said hurts, and Context.Pain must come from a record
// rather than from an inference — SPEC §4.12's whole point is that Babel learns
// from attributed context and not from engagement. And the inventory can count
// complaints instead of reporting them uncounted.
//
// What it deliberately does not do is attribute a complaint to an artifact by
// matching its text. A complaint names no entity, and guessing which proposal
// an operator's sentence was about is exactly the invented preference this
// package refuses; so complaints raise the deployment's recorded pain floor and
// are reported as such, while per-subject pain comes from the ledger's own
// entity-targeted records.
func WithComplaints(store *complaint.Store) SourceOption {
	return func(s *babelSource) { s.complaints = store }
}

// WithProducedCounter lends a way to count one non-reviewable produced kind.
//
// It is a function rather than a store because the kinds in question live in
// packages this one has no business importing — run preparations and receipts,
// most of all — and because the caller assembling the service already holds
// their handles. An unwired kind stays LocalCounted false, which is an honest
// "this build cannot count it here" rather than a zero.
func WithProducedCounter(kind string, count func(context.Context) (int, error)) SourceOption {
	return func(s *babelSource) {
		if kind == "" || count == nil {
			return
		}
		if s.counters == nil {
			s.counters = make(map[string]func(context.Context) (int, error))
		}
		s.counters[kind] = count
	}
}

// WithFocusPolicyVersion names the Reality focus policy version context is
// evaluated against.
//
// It is named rather than discovered for internal/reality's own reason: a
// decision taken against "whatever the newest policy is" cannot be re-derived
// once a newer one is installed, and a recommendation an operator cannot
// reproduce is not one they can argue with. The default is the version this
// build ships.
func WithFocusPolicyVersion(version int) SourceOption {
	return func(s *babelSource) { s.focusVersion = version }
}

// NewSource builds the read adapter.
//
// front is required: it is this machine's own analysis and the reason a single
// instance has anything to review at all. realityStore may be nil on a machine
// whose ledger did not open, and remote may be nil in local mode.
//
// A nil remote means local mode *intentionally*. A shared mode whose startup
// failed must not arrive here as nil, because this package cannot tell the two
// apart and would report the outage as a configuration choice; the caller knows
// which it has and constructs accordingly (see SourceStatus.LocalOnly).
func NewSource(front *frontier.Store, realityStore *reality.Store, remote FleetSource,
	opts ...SourceOption) Source {
	src := &babelSource{
		front:        front,
		ledger:       realityStore,
		remote:       remote,
		focusVersion: reality.DefaultFocusRules().Version,
		opened:       make(map[string]openedRecord),
	}
	for _, opt := range opts {
		opt(src)
	}
	src.status.LocalOnly = remote == nil
	return src
}

// babelSource is the Source over this deployment's own output.
type babelSource struct {
	front        *frontier.Store
	ledger       *reality.Store
	remote       FleetSource
	complaints   *complaint.Store
	counters     map[string]func(context.Context) (int, error)
	focusVersion int

	// mu guards the decode cache and the status, both of which are written
	// by a refresh and read by whatever surface asks next.
	mu     sync.Mutex
	opened map[string]openedRecord
	status SourceStatus
}

// openedRecord is one decoded remote record, remembered by the digest of the
// sealed object it came from.
//
// Keying on the digest rather than on the record id is what makes the cache
// safe rather than merely fast: analysis_records is append-only and a record id
// is bound to one object, so a matching digest means the same bytes and a
// differing digest means a record this instance has not decoded. There is no
// invalidation to get wrong.
type openedRecord struct {
	digest string
	// artifact is the decoded reviewable artifact, nil for a record that is
	// not one.
	artifact *Artifact
	// record is the decoded evaluation record, nil for anything else.
	record *Record
	// answer is a decoded review answer: the disposition another host
	// recorded, which is what lets this instance derive a remote artifact's
	// review status instead of showing every remote proposal as new.
	answer *remoteAnswer
	// unopened is why this record could not be opened, empty on success.
	unopened string
}

// remoteAnswer is one operator review decision another host published.
type remoteAnswer struct {
	subject   Subject
	decision  string
	refine    bool
	createdAt time.Time
}

// Status reports how complete the last read was.
func (s *babelSource) Status() SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Artifacts enumerates the head revision of every covered artifact this
// deployment can see.
//
// Local first and unconditionally. The fleet read then adds what other hosts
// published, and a record this machine already holds durably is skipped there:
// the durable row is the better copy — it is current where a published record
// is a snapshot at staging time — which is the same tie-break internal/index
// uses when a fleet read hands a machine its own records back.
//
// Heads are computed from the revision chains rather than asked for one at a
// time. A chain has exactly one leaf (frontier.ErrSuperseded guarantees it), so
// "no record in this chain names me as its ancestor" identifies it in one pass
// over the set instead of one query per record.
func (s *babelSource) Artifacts(ctx context.Context) ([]Artifact, error) {
	if s.front == nil {
		return nil, fmt.Errorf("%w: evaluation source has no durable frontier", ErrUnavailable)
	}
	var status SourceStatus
	status.LocalOnly = s.remote == nil
	status.ReadAt = time.Now().UTC()

	local, err := s.localArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[Subject]struct{}, len(local))
	for _, artifact := range local {
		seen[artifact.Subject] = struct{}{}
	}

	remote, answers, degradation := s.remoteArtifacts(ctx, seen, &status)
	out := append(local, remote...)

	// Review status for a remote artifact is derived from the review
	// answers other hosts published about it. Without this every remote
	// proposal would read as `new`, which would put an accepted proposal in
	// the Open lane on a non-producing instance and make lanes disagree
	// across machines about the same record.
	applyRemoteReviewStatus(out, answers)

	if err := s.attachContext(ctx, out, status.ReadAt, &status); err != nil {
		return nil, err
	}
	if degradation != "" {
		status.Unavailable = joinReasons(status.Unavailable, degradation)
	}
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()

	sortArtifacts(out)
	return out, nil
}

// localArtifacts enumerates this machine's durable analysis.
//
// Outputs is the enumeration for hypotheses, observations and findings: it
// already returns exactly the head revisions with their chain identity, so
// nothing here re-derives either. Proposals are absent from it on purpose —
// internal/frontier refuses a proposal a searchable output because its text is
// its findings' text restated — so they are paged out of the proposal listing
// with LeavesOnly, which is the same "head revisions only" rule stated in that
// enumeration's own terms.
func (s *babelSource) localArtifacts(ctx context.Context) ([]Artifact, error) {
	outputs, err := s.front.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: enumerate local frontier: %w", err)
	}
	out := make([]Artifact, 0, len(outputs))
	for _, output := range outputs {
		kind, ok := subjectKindOf(output.Kind)
		if !ok {
			continue
		}
		artifact, err := s.buildLocal(ctx, Subject{Kind: kind, ID: output.ID},
			output.RootID, output.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}

	for offset := 0; ; {
		proposals, total, err := s.front.Proposals(ctx, frontier.ListFilter{
			LeavesOnly: true,
			Limit:      frontier.MaxListLimit,
			Offset:     offset,
		})
		if err != nil {
			return nil, fmt.Errorf("evaluation: enumerate local proposals: %w", err)
		}
		for _, proposal := range proposals {
			artifact, err := s.proposalArtifact(ctx, proposal, "")
			if err != nil {
				return nil, err
			}
			out = append(out, artifact)
		}
		offset += len(proposals)
		if len(proposals) == 0 || offset >= total {
			break
		}
	}
	return out, nil
}

// buildLocal reads one local artifact whole.
//
// rootID and headID are passed when the caller already knows them, which the
// bulk enumeration does: Outputs returns heads with their chain identity, so
// resolving the chain again would be one extra query per record for an answer
// already in hand. The resolver path leaves them empty and pays for the lookup,
// because there the whole question is whether the revision it was handed is
// still the head.
func (s *babelSource) buildLocal(ctx context.Context, subject Subject, rootID, headID string) (Artifact, error) {
	entity, ok := entityTypeOf(subject.Kind)
	if !ok {
		return Artifact{}, fmt.Errorf("%w: subject kind %q is not a frontier record", ErrInvalid, subject.Kind)
	}
	if rootID == "" || headID == "" {
		chain, err := s.front.Revisions(ctx, frontier.Ref{Type: entity, ID: subject.ID})
		if err != nil {
			if errors.Is(err, frontier.ErrUnknownEntity) {
				return Artifact{}, fmt.Errorf("%w: %s %s", ErrNotFound, subject.Kind, subject.ID)
			}
			return Artifact{}, fmt.Errorf("evaluation: read revision chain: %w", err)
		}
		if len(chain) == 0 {
			return Artifact{}, fmt.Errorf("%w: %s %s has no revision chain", ErrNotFound, subject.Kind, subject.ID)
		}
		rootID, headID = chain[0].RootID, chain[len(chain)-1].Entity.ID
	}
	artifact := Artifact{Subject: subject, RootID: rootID, HeadID: headID}

	switch subject.Kind {
	case SubjectKindHypothesis:
		record, err := s.front.Hypothesis(ctx, subject.ID)
		if err != nil {
			return Artifact{}, wrapRead(err, subject)
		}
		artifact.RunID = record.RunID
		artifact.CreatedAt = record.CreatedAt
		artifact.Title = summarizeLine(record.Payload.Statement)
		artifact.Status = string(record.Status)
		body, err := json.Marshal(record.Payload)
		if err != nil {
			return Artifact{}, fmt.Errorf("evaluation: encode hypothesis payload: %w", err)
		}
		artifact.Body = body
		related, err := s.hypothesisRelated(ctx, subject.ID)
		if err != nil {
			return Artifact{}, err
		}
		artifact.Related = related
	case SubjectKindObservation:
		record, err := s.front.Observation(ctx, subject.ID)
		if err != nil {
			return Artifact{}, wrapRead(err, subject)
		}
		artifact.RunID = record.RunID
		artifact.CreatedAt = record.CreatedAt
		artifact.Title = summarizeLine(record.Payload.Claim)
		artifact.Evidence = append(append([]frontier.Evidence{}, record.Payload.Evidence...),
			record.Payload.CounterEvidence...)
		body, err := json.Marshal(record.Payload)
		if err != nil {
			return Artifact{}, fmt.Errorf("evaluation: encode observation payload: %w", err)
		}
		artifact.Body = body
		artifact.Related = []Subject{{Kind: SubjectKindHypothesis, ID: record.HypothesisID}}
	case SubjectKindFinding:
		record, err := s.front.Finding(ctx, subject.ID)
		if err != nil {
			return Artifact{}, wrapRead(err, subject)
		}
		artifact.RunID = record.RunID
		artifact.CreatedAt = record.CreatedAt
		artifact.Title = summarizeLine(record.Payload.Title)
		artifact.Evidence = append([]frontier.Evidence{}, record.Payload.CounterEvidence...)
		body, err := json.Marshal(record.Payload)
		if err != nil {
			return Artifact{}, fmt.Errorf("evaluation: encode finding payload: %w", err)
		}
		artifact.Body = body
		for _, id := range record.ObservationIDs {
			artifact.Related = append(artifact.Related, Subject{Kind: SubjectKindObservation, ID: id})
		}
		for _, id := range record.HypothesisIDs {
			artifact.Related = append(artifact.Related, Subject{Kind: SubjectKindHypothesis, ID: id})
		}
	case SubjectKindProposal:
		record, err := s.front.Proposal(ctx, subject.ID)
		if err != nil {
			return Artifact{}, wrapRead(err, subject)
		}
		return s.proposalArtifact(ctx, record, rootID)
	default:
		return Artifact{}, fmt.Errorf("%w: subject kind %q", ErrInvalid, subject.Kind)
	}

	if reviewableEntity(entity) {
		status, err := s.reviewStatus(ctx, entity, subject)
		if err != nil {
			return Artifact{}, err
		}
		artifact.ReviewStatus = status
	}
	return artifact, nil
}

// proposalArtifact builds a proposal's artifact, including the verification
// criteria the proposal itself suggested.
//
// Those criteria are the proposal's suggestion and nothing more. §E6 is
// explicit that Babel may suggest criteria but cannot rewrite its target and
// verify itself against the replacement, so they arrive here with derived
// identifiers and no authority: an outcome assessment names the criteria
// *version* it was judged against, and an operator's criteria record supersedes
// the suggestion without editing the proposal.
func (s *babelSource) proposalArtifact(ctx context.Context, record frontier.Proposal, rootID string) (Artifact, error) {
	subject := Subject{Kind: SubjectKindProposal, ID: record.ID}
	if rootID == "" {
		chain, err := s.front.Revisions(ctx, frontier.Ref{Type: frontier.EntityProposal, ID: record.ID})
		if err != nil {
			return Artifact{}, fmt.Errorf("evaluation: read proposal chain: %w", err)
		}
		if len(chain) == 0 {
			return Artifact{}, fmt.Errorf("%w: proposal %s has no revision chain", ErrNotFound, record.ID)
		}
		rootID = chain[0].RootID
	}
	head, err := s.front.Head(ctx, frontier.Ref{Type: frontier.EntityProposal, ID: record.ID})
	if err != nil {
		return Artifact{}, fmt.Errorf("evaluation: read proposal head: %w", err)
	}
	body, err := json.Marshal(record.Payload)
	if err != nil {
		return Artifact{}, fmt.Errorf("evaluation: encode proposal payload: %w", err)
	}
	artifact := Artifact{
		Subject:      subject,
		RootID:       rootID,
		HeadID:       head.ID,
		RunID:        record.RunID,
		CreatedAt:    record.CreatedAt,
		Title:        summarizeLine(record.Payload.Title),
		Body:         body,
		ReviewStatus: string(record.ReviewStatus),
		Status:       string(record.Form),
		Criteria:     suggestedCriteria(record.ID, record.Payload.VerificationCriteria),
	}
	artifact.Evidence = append(append([]frontier.Evidence{}, record.Payload.Supporting...),
		record.Payload.Conflicting...)
	for _, id := range record.FindingIDs {
		artifact.Related = append(artifact.Related, Subject{Kind: SubjectKindFinding, ID: id})
	}
	for _, id := range record.HypothesisIDs {
		artifact.Related = append(artifact.Related, Subject{Kind: SubjectKindHypothesis, ID: id})
	}
	return artifact, nil
}

// hypothesisRelated collects what a candidate is joined to: its typed links in
// both directions, and the remedies that address it.
//
// Both directions, because §4.2's lineage is traversable from either end and a
// contradicted candidate has to be reachable from the contradiction. The
// remedies are here because #114's competing-remedies question — which
// alternatives has anybody offered for this claim — is what §E5's grouped
// comparison reads, and it is asserted on the proposal rather than derivable
// from the hypothesis.
func (s *babelSource) hypothesisRelated(ctx context.Context, id string) ([]Subject, error) {
	out := make([]Subject, 0, 4)
	from, err := s.front.LinksFrom(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read hypothesis links: %w", err)
	}
	for _, link := range from {
		out = append(out, Subject{Kind: SubjectKindHypothesis, ID: link.ToID})
	}
	to, err := s.front.LinksTo(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read hypothesis links: %w", err)
	}
	for _, link := range to {
		out = append(out, Subject{Kind: SubjectKindHypothesis, ID: link.FromID})
	}
	remedies, err := s.front.ProposalsAddressing(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read addressing proposals: %w", err)
	}
	for _, remedy := range remedies {
		out = append(out, Subject{Kind: SubjectKindProposal, ID: remedy.ID})
	}
	return out, nil
}

// reviewStatus reads the operator's derived review state for a local record.
func (s *babelSource) reviewStatus(ctx context.Context, entity frontier.EntityType, subject Subject) (string, error) {
	status, err := s.front.ReviewStatus(ctx, frontier.Ref{Type: entity, ID: subject.ID})
	if err != nil {
		if errors.Is(err, frontier.ErrUnknownEntity) {
			return "", nil
		}
		return "", fmt.Errorf("evaluation: read review status of %s %s: %w", subject.Kind, subject.ID, err)
	}
	return string(status), nil
}

// Artifact resolves one subject, current or superseded.
//
// A superseded revision resolves rather than failing, and that is the whole
// reason this is not just a lookup in the Artifacts slice. §E1 binds a vote to
// the exact wording read, so an assignment taken against revision n must still
// be servable after n+1 exists — and the returned artifact's HeadID then
// differs from its subject id, which is exactly how a stale result is told
// apart from current coverage rather than silently endorsing the new wording.
func (s *babelSource) Artifact(ctx context.Context, subject Subject) (Artifact, error) {
	if subject.ID == "" {
		return Artifact{}, fmt.Errorf("%w: subject names no record", ErrInvalid)
	}
	if _, ok := entityTypeOf(subject.Kind); ok {
		if s.front == nil {
			return Artifact{}, fmt.Errorf("%w: evaluation source has no durable frontier", ErrUnavailable)
		}
		artifact, err := s.buildLocal(ctx, subject, "", "")
		if err == nil {
			batch := []Artifact{artifact}
			if err := s.attachContext(ctx, batch, time.Now().UTC(), nil); err != nil {
				return Artifact{}, err
			}
			return batch[0], nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Artifact{}, err
		}
		return s.remoteArtifact(ctx, subject)
	}
	if subject.Kind == SubjectKindEvaluation {
		// A meta-review subject is an evaluation record, and the record
		// is the artifact: Store.Record answers for local ones and the
		// fleet for the rest, so resolving it here would be a third
		// answer to one question.
		return Artifact{}, fmt.Errorf("%w: evaluation record %s is resolved as a record, not an artifact",
			ErrInvalid, subject.ID)
	}
	return Artifact{}, fmt.Errorf("%w: subject kind %q", ErrInvalid, subject.Kind)
}

// attachContext fills in each artifact's Reality context and context version.
//
// It runs over the whole batch rather than per artifact because the ledger reads
// it needs — the unresolved questions, the focus policy — are one query each for
// the batch and one query each per artifact otherwise. status may be nil when
// the caller is resolving a single subject and has no status to update.
func (s *babelSource) attachContext(ctx context.Context, artifacts []Artifact,
	asOf time.Time, status *SourceStatus) error {
	view, err := s.openLedger(ctx, asOf)
	if err != nil {
		return err
	}
	names := resolveNames(artifacts)
	filed, err := s.filedTopics(ctx, artifacts)
	if err != nil {
		return err
	}
	for i := range artifacts {
		artifacts[i].Context, err = view.contextFor(ctx, names[i], filed[i], asOf)
		if err != nil {
			return err
		}
		artifacts[i].ContextVersion = artifacts[i].Context.Version
	}
	if status != nil && view.unavailable != "" {
		status.Unavailable = joinReasons(status.Unavailable, view.unavailable)
	}
	return nil
}

// filedTopics reports, per artifact, the Reality entities the record is filed
// under (§4.13).
//
// It is the second half of "what this record is about", and the half the
// operator actually stated. resolveNames reads terms a run wrote down and asks
// the ledger to resolve them; a filing is already an entity id, asserted by
// the run that produced the record, by the triage recipe or by the operator
// himself — so a topic the operator paused moves the draws away from its
// records whether or not their labels happen to spell its aliases.
//
// Withdrawn filings and filings under a retired topic are absent, because
// internal/frontier reads them that way: retiring a topic returns its records
// to the backlog, and a record whose only topic was retired is one whose
// context is again unstated rather than one restricted by a subject that is no
// longer one.
//
// A remote record has no local filing and gets none: the frontier this reads
// is this machine's, and a record another host published is filed wherever
// that host filed it. Reporting nothing is the honest answer rather than a
// gap, because the topic set of a record this instance did not write is not
// something it can observe.
func (s *babelSource) filedTopics(ctx context.Context, artifacts []Artifact) ([][]string, error) {
	filed := make([][]string, len(artifacts))
	if s.front == nil {
		return filed, nil
	}
	for i := range artifacts {
		kind, ok := entityTypeOf(artifacts[i].Subject.Kind)
		if !ok {
			continue
		}
		entities, err := s.front.EntitiesFiled(ctx,
			frontier.Ref{Type: kind, ID: artifacts[i].Subject.ID})
		if err != nil {
			return nil, fmt.Errorf("evaluation: read the topics of %s %s: %w",
				artifacts[i].Subject.Kind, artifacts[i].Subject.ID, err)
		}
		filed[i] = entities
	}
	return filed, nil
}

// resolveNames reports, per artifact, the terms its subject is known by.
//
// They are read off structured fields the producing run recorded — provisional
// labels, a finding's scope, a proposal's suggested target systems — and never
// off prose. §4.8 puts the operator in charge of which spellings mean which
// entity, so matching a claim's sentence against entity names would be this
// package inventing the mapping the ledger exists to own, and Main's rule that
// source-derived current work and pain must not rest on generated title wording
// is enforced here by there being no path from prose to a name at all.
//
// An observation inherits its hypothesis's labels, because an observation names
// no subject of its own: it develops a candidate, and the candidate is what the
// operator recorded an allowance about. The parent is looked up in the batch
// rather than in the store — the batch is the whole inventory, so the lookup is
// a map hit instead of one query per observation, and an observation whose
// parent is not in the batch gets no names and an explicit Unknown rather than
// a permissive default.
func resolveNames(artifacts []Artifact) [][]string {
	own := make([][]string, len(artifacts))
	index := make(map[Subject]int, len(artifacts))
	for i := range artifacts {
		own[i] = recordedNames(artifacts[i])
		index[artifacts[i].Subject] = i
	}
	out := make([][]string, len(artifacts))
	for i := range artifacts {
		out[i] = own[i]
		if artifacts[i].Subject.Kind != SubjectKindObservation || len(out[i]) > 0 {
			continue
		}
		for _, related := range artifacts[i].Related {
			if related.Kind != SubjectKindHypothesis {
				continue
			}
			if parent, ok := index[related]; ok && len(own[parent]) > 0 {
				out[i] = own[parent]
				break
			}
		}
	}
	return out
}

// recordedNames reads one artifact's own recorded names.
//
// It is a pure function of the artifact so that the projection can store the
// result and the worker's pre-launch expenditure gate can read it off the
// assignment, without either of them reaching back into a store.
func recordedNames(artifact Artifact) []string {
	var names []string
	switch artifact.Subject.Kind {
	case SubjectKindHypothesis:
		var payload frontier.HypothesisPayload
		if json.Unmarshal(artifact.Body, &payload) == nil {
			names = payload.ProvisionalLabels
		}
	case SubjectKindFinding:
		var payload frontier.FindingPayload
		if json.Unmarshal(artifact.Body, &payload) == nil {
			names = payload.Scope
		}
	case SubjectKindProposal:
		var payload frontier.ProposalPayload
		if json.Unmarshal(artifact.Body, &payload) == nil {
			for _, target := range payload.Targets {
				names = append(names, target.System)
			}
		}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// remoteArtifacts adds the covered artifacts other hosts published.
//
// The catalog listing is paged to exhaustion and every row is examined, but only
// the rows whose sealed object this instance has not already decoded are opened.
// That is the incremental half: a deployment's committed analysis grows
// monotonically and a record's bytes never change, so a steady-state refresh
// opens exactly the records that committed since the last one.
//
// seen holds the subjects the local store already answered for, and they are
// skipped rather than merged.
func (s *babelSource) remoteArtifacts(ctx context.Context, seen map[Subject]struct{},
	status *SourceStatus) ([]Artifact, []remoteAnswer, string) {
	if s.remote == nil {
		return nil, nil, ""
	}
	rows, err := s.listFleet(ctx, []sharedcatalog.RecordKind{
		sharedcatalog.KindHypothesis, sharedcatalog.KindObservation,
		sharedcatalog.KindFinding, sharedcatalog.KindProposal,
		sharedcatalog.KindDisposition,
	})
	if err != nil {
		// A catalog this instance cannot read is a degradation, never an
		// empty fleet: reporting no remote artifacts would make another
		// host's never-reviewed observation look reviewed-by-absence.
		return nil, nil, fmt.Sprintf("fleet artifact listing unavailable: %v", err)
	}

	opened := s.openAll(ctx, rows)
	var (
		artifacts []Artifact
		answers   []remoteAnswer
		chains    = make(map[string][]Artifact)
		ancestors = make(map[string]struct{})
	)
	for _, row := range rows {
		entry := opened[row.Record.RecordID]
		if entry.unopened != "" {
			status.Unopened++
			continue
		}
		if row.HostID == "" {
			status.Unattributed++
		}
		switch {
		case entry.answer != nil:
			answers = append(answers, *entry.answer)
		case entry.artifact != nil:
			artifact := *entry.artifact
			if _, ok := seen[artifact.Subject]; ok {
				continue
			}
			chains[artifact.RootID] = append(chains[artifact.RootID], artifact)
			if artifact.HeadID != "" && artifact.HeadID != artifact.Subject.ID {
				// HeadID carries the ancestor on the wire path;
				// see decodePublished.
				ancestors[artifact.HeadID] = struct{}{}
			}
		}
	}
	// A chain's head is the revision nothing in the chain supersedes. It is
	// computed over the set rather than asked of the producing host, which
	// is the only option available here: the producer's durable store is not
	// reachable and its published records are snapshots.
	for _, chain := range chains {
		for _, artifact := range chain {
			if _, superseded := ancestors[artifact.Subject.ID]; superseded {
				continue
			}
			artifact.HeadID = artifact.Subject.ID
			artifacts = append(artifacts, artifact)
		}
	}
	if status.Unopened > 0 {
		return artifacts, answers, fmt.Sprintf(
			"%d fleet record(s) could not be opened on this instance and are absent from the inventory",
			status.Unopened)
	}
	return artifacts, answers, ""
}

// remoteArtifact resolves one subject the local store does not hold.
func (s *babelSource) remoteArtifact(ctx context.Context, subject Subject) (Artifact, error) {
	if s.remote == nil {
		return Artifact{}, fmt.Errorf("%w: %s %s is not in the local durable store and this "+
			"instance runs in local mode", ErrNotFound, subject.Kind, subject.ID)
	}
	rows, err := s.remote.Records(ctx, sharedcatalog.RecordFilter{
		RecordIDs: []string{subject.ID},
		Limit:     1,
	})
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: fleet lookup of %s %s failed: %v",
			ErrUnavailable, subject.Kind, subject.ID, err)
	}
	if len(rows) == 0 {
		return Artifact{}, fmt.Errorf("%w: %s %s", ErrNotFound, subject.Kind, subject.ID)
	}
	entry := s.open(ctx, rows[0])
	if entry.unopened != "" {
		return Artifact{}, fmt.Errorf("%w: %s %s could not be opened on this instance: %s",
			ErrUnavailable, subject.Kind, subject.ID, entry.unopened)
	}
	if entry.artifact == nil {
		return Artifact{}, fmt.Errorf("%w: record %s is not a reviewable artifact", ErrInvalid, subject.ID)
	}
	batch := []Artifact{*entry.artifact}
	if err := s.attachContext(ctx, batch, time.Now().UTC(), nil); err != nil {
		return Artifact{}, err
	}
	return batch[0], nil
}

// EvaluationRecords reads the evaluation records committed by the fleet.
//
// It returns other hosts' publications, not this machine's: the local store's
// own journal is authoritative for local records and reading them back through
// the catalog would be a second, staler copy. What arrives here is what a
// non-producing instance cannot otherwise know — remote assessments, operator
// criteria and feedback, policies, and the assignment/attempt/checkpoint
// journal that lets coverage be rebuilt rather than guessed from vote counts.
//
// A nil remote returns nothing and no error, which is local mode's honest
// answer: there is no fleet to read.
func (s *babelSource) EvaluationRecords(ctx context.Context) ([]Record, error) {
	if s.remote == nil {
		return nil, nil
	}
	rows, err := s.listFleet(ctx, []sharedcatalog.RecordKind{sharedcatalog.KindEvaluation})
	if err != nil {
		return nil, fmt.Errorf("%w: fleet evaluation listing failed: %v", ErrUnavailable, err)
	}
	opened := s.openAll(ctx, rows)
	out := make([]Record, 0, len(rows))
	var unopened int
	for _, row := range rows {
		entry := opened[row.Record.RecordID]
		if entry.unopened != "" || entry.record == nil {
			unopened++
			continue
		}
		out = append(out, *entry.record)
	}
	s.mu.Lock()
	s.status.Unopened += unopened
	if unopened > 0 {
		s.status.Unavailable = joinReasons(s.status.Unavailable, fmt.Sprintf(
			"%d fleet evaluation record(s) could not be opened on this instance", unopened))
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// EvaluationRecord resolves one evaluation record by id across the deployment.
//
// The three outcomes are deliberately distinguishable. A record the fleet does
// not hold is ErrNotFound, which is what lets a store refuse a criteria record
// naming a publication nobody made. A fleet this instance could not query is
// ErrUnavailable, because "I could not look" must never be recorded as "it does
// not exist". And a record whose object will not open is also ErrUnavailable,
// for the same reason applied one layer down.
func (s *babelSource) EvaluationRecord(ctx context.Context, id string) (Record, error) {
	if id == "" {
		return Record{}, fmt.Errorf("%w: record id is empty", ErrInvalid)
	}
	if s.remote == nil {
		return Record{}, fmt.Errorf("%w: record %s is not published to any fleet this instance reads",
			ErrNotFound, id)
	}
	rows, err := s.remote.Records(ctx, sharedcatalog.RecordFilter{
		Kinds:     []sharedcatalog.RecordKind{sharedcatalog.KindEvaluation},
		RecordIDs: []string{id},
		Limit:     1,
	})
	if err != nil {
		return Record{}, fmt.Errorf("%w: fleet lookup of record %s failed: %v", ErrUnavailable, id, err)
	}
	if len(rows) == 0 {
		return Record{}, fmt.Errorf("%w: evaluation record %s", ErrNotFound, id)
	}
	entry := s.open(ctx, rows[0])
	if entry.unopened != "" {
		return Record{}, fmt.Errorf("%w: record %s could not be opened on this instance: %s",
			ErrUnavailable, id, entry.unopened)
	}
	if entry.record == nil {
		return Record{}, fmt.Errorf("%w: record %s is not an evaluation record", ErrInvalid, id)
	}
	return *entry.record, nil
}

// Produced enumerates every produced record kind, reviewable or not.
func (s *babelSource) Produced(ctx context.Context) ([]KindInventory, error) {
	index := make(map[string]*KindInventory)
	order := make([]string, 0, len(Kinds())+len(NonReviewableKinds()))
	for _, kind := range Kinds() {
		index[kind] = &KindInventory{Kind: kind, Reviewable: true}
		order = append(order, kind)
	}
	for _, kind := range NonReviewableKinds() {
		index[kind] = &KindInventory{Kind: kind, Reason: KindUnreviewableReason(kind)}
		order = append(order, kind)
	}

	// Local counts for the reviewable kinds come from the inventory itself
	// rather than from a second enumeration, so the two cannot disagree
	// about how many observations this machine holds.
	artifacts, err := s.localArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if entry, ok := index[artifact.Subject.Kind]; ok {
			entry.Local++
			entry.LocalCounted = true
		}
	}
	for _, kind := range Kinds() {
		if entity, ok := entityTypeOf(kind); ok && entity != "" {
			index[kind].LocalCounted = true
		}
	}

	if s.complaints != nil {
		heads, err := s.complaints.Heads(ctx)
		if err != nil {
			return nil, fmt.Errorf("evaluation: count local complaints: %w", err)
		}
		index["complaint"].Local, index["complaint"].LocalCounted = len(heads), true
	}
	for kind, count := range s.counters {
		entry, ok := index[kind]
		if !ok {
			entry = &KindInventory{Kind: kind, Reason: KindUnreviewableReason(kind)}
			index[kind] = entry
			order = append(order, kind)
		}
		total, err := count(ctx)
		if err != nil {
			return nil, fmt.Errorf("evaluation: count local %s records: %w", kind, err)
		}
		entry.Local, entry.LocalCounted = total, true
	}

	if s.remote != nil {
		for _, kind := range catalogKinds() {
			rows, err := s.listFleet(ctx, []sharedcatalog.RecordKind{kind.catalog})
			if err != nil {
				// One unreadable catalog kind leaves that row
				// uncounted rather than failing the inventory,
				// which is the same per-record honesty the rest
				// of this file keeps.
				continue
			}
			entry, ok := index[kind.inventory]
			if !ok {
				entry = &KindInventory{
					Kind:   kind.inventory,
					Reason: KindUnreviewableReason(kind.inventory),
				}
				index[kind.inventory] = entry
				order = append(order, kind.inventory)
			}
			entry.Fleet, entry.FleetCounted = len(rows), true
		}
	}

	slices.Sort(order)
	order = slices.Compact(order)
	out := make([]KindInventory, 0, len(order))
	for _, kind := range order {
		out = append(out, *index[kind])
	}
	return out, nil
}

// catalogKind pairs a catalog record kind with the inventory row it belongs to.
//
// The two vocabularies are not the same and must not be assumed to be: the
// catalog's `disposition` kind carries both internal/frontier's review answers
// and internal/disposition's ledger, and `evaluation` carries this package's
// whole record family. The mapping is written out so an added catalog kind is a
// compile-time visit to this list rather than a silently uncounted row.
type catalogKind struct {
	catalog   sharedcatalog.RecordKind
	inventory string
}

func catalogKinds() []catalogKind {
	return []catalogKind{
		{sharedcatalog.KindHypothesis, SubjectKindHypothesis},
		{sharedcatalog.KindObservation, SubjectKindObservation},
		{sharedcatalog.KindFinding, SubjectKindFinding},
		{sharedcatalog.KindProposal, SubjectKindProposal},
		{sharedcatalog.KindEvaluation, SubjectKindEvaluation},
		{sharedcatalog.KindLink, "link"},
		{sharedcatalog.KindDisposition, "disposition"},
		{sharedcatalog.KindContext, "context"},
		{sharedcatalog.KindPreparation, "preparation"},
		{sharedcatalog.KindReceipt, "receipt"},
		{sharedcatalog.KindComplaint, "complaint"},
	}
}

// listFleet pages one kind filter to exhaustion.
//
// Exhaustion rather than a cap, for internal/fleet's reason: the set is the
// analysis a deployment has produced, which is thousands of records in the same
// order of magnitude as the local durable store, and a cap would turn a
// complete inventory into a partial one that looks complete.
func (s *babelSource) listFleet(ctx context.Context, kinds []sharedcatalog.RecordKind) ([]fleet.Record, error) {
	var out []fleet.Record
	for offset := 0; ; offset += fleetPageSize {
		page, err := s.remote.Records(ctx, sharedcatalog.RecordFilter{
			Kinds:  kinds,
			Limit:  fleetPageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < fleetPageSize {
			return out, nil
		}
	}
}

// openAll opens the records whose sealed objects this instance has not already
// decoded, a bounded number at a time.
//
// The cache lookup happens before the fan-out so an unchanged refresh starts no
// goroutines at all, and the result map is keyed by record id so callers index
// it beside the rows they listed.
func (s *babelSource) openAll(ctx context.Context, rows []fleet.Record) map[string]openedRecord {
	out := make(map[string]openedRecord, len(rows))
	pending := make([]fleet.Record, 0, len(rows))

	s.mu.Lock()
	for _, row := range rows {
		if entry, ok := s.opened[row.Record.RecordID]; ok && entry.digest == row.Record.ObjectDigest {
			out[row.Record.RecordID] = entry
			continue
		}
		pending = append(pending, row)
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return out
	}

	var (
		wg      sync.WaitGroup
		gate    = make(chan struct{}, maxConcurrentOpens)
		results = make([]openedRecord, len(pending))
	)
	for i, row := range pending {
		wg.Add(1)
		go func(i int, row fleet.Record) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			results[i] = s.decode(ctx, row)
		}(i, row)
	}
	wg.Wait()

	s.mu.Lock()
	for i, row := range pending {
		s.opened[row.Record.RecordID] = results[i]
		out[row.Record.RecordID] = results[i]
	}
	s.mu.Unlock()
	return out
}

// open opens exactly one record, through the same cache.
func (s *babelSource) open(ctx context.Context, row fleet.Record) openedRecord {
	return s.openAll(ctx, []fleet.Record{row})[row.Record.RecordID]
}

// decode fetches, opens and decodes one remote record.
//
// Every failure becomes an unopened reason rather than an error, because the
// caller's question is "what does the fleet hold" and "eleven records, one
// sealed with a key you do not have" is the answer to it.
func (s *babelSource) decode(ctx context.Context, row fleet.Record) openedRecord {
	entry := openedRecord{digest: row.Record.ObjectDigest}
	opened, err := s.remote.Open(ctx, sharedcatalog.FleetRecord{Record: row.Record})
	if err != nil {
		entry.unopened = err.Error()
		return entry
	}
	if opened.Unopened != "" {
		entry.unopened = opened.Unopened
		return entry
	}
	switch row.Record.Kind {
	case sharedcatalog.KindEvaluation:
		record, err := Decode(opened.Content)
		if err != nil {
			entry.unopened = fmt.Sprintf("decode evaluation record: %v", err)
			return entry
		}
		// The catalog row's id is authenticated by OpenRecord; the
		// decoded record's is not. A payload claiming a different id
		// would let one host's publication masquerade as another
		// record, so the two must agree before anything reads it.
		if record.ID != row.Record.RecordID {
			entry.unopened = fmt.Sprintf(
				"evaluation record claims id %q but committed as %q",
				record.ID, row.Record.RecordID)
			return entry
		}
		entry.record = &record
	case sharedcatalog.KindDisposition:
		if opened.Published == nil || opened.Published.Kind != frontier.PublishedReviewAnswer {
			// internal/disposition's own ledger shares this catalog
			// kind. It is not a frontier review answer and is not
			// review authority over an artifact, so it is skipped
			// rather than misread.
			return entry
		}
		entry.answer = decodeAnswer(*opened.Published)
	default:
		if opened.Published == nil {
			entry.unopened = "record opened but carries no frontier projection"
			return entry
		}
		artifact, err := decodePublished(*opened.Published, row)
		if err != nil {
			entry.unopened = err.Error()
			return entry
		}
		entry.artifact = &artifact
	}
	return entry
}

// decodePublished turns one remote frontier record into an artifact.
//
// HeadID carries the record's ancestor on the way out of here, which is a
// deliberate and local overload: remoteArtifacts needs the supersession edges to
// find each chain's leaf, and it rewrites HeadID to the head it computed before
// any artifact leaves the package. Carrying a second field for one intermediate
// step would put a value on the exported type that is meaningless everywhere
// else.
func decodePublished(published frontier.PublishedRecord, row fleet.Record) (Artifact, error) {
	kind, ok := publishedSubjectKind(published.Kind)
	if !ok {
		return Artifact{}, fmt.Errorf("record kind %q is not a reviewable artifact", published.Kind)
	}
	if published.ID != row.Record.RecordID {
		return Artifact{}, fmt.Errorf("published record claims id %q but committed as %q",
			published.ID, row.Record.RecordID)
	}
	artifact := Artifact{
		Subject:   Subject{Kind: kind, ID: published.ID},
		RootID:    published.RootID,
		HeadID:    published.Ancestor,
		RunID:     published.RunID,
		CreatedAt: published.CreatedAt,
		Status:    string(published.Status),
		Body:      published.Payload,
	}
	switch kind {
	case SubjectKindHypothesis:
		var payload frontier.HypothesisPayload
		if err := json.Unmarshal(published.Payload, &payload); err != nil {
			return Artifact{}, fmt.Errorf("decode remote hypothesis: %w", err)
		}
		artifact.Title = summarizeLine(payload.Statement)
	case SubjectKindObservation:
		var payload frontier.ObservationPayload
		if err := json.Unmarshal(published.Payload, &payload); err != nil {
			return Artifact{}, fmt.Errorf("decode remote observation: %w", err)
		}
		artifact.Title = summarizeLine(payload.Claim)
		artifact.Evidence = append(append([]frontier.Evidence{}, payload.Evidence...),
			payload.CounterEvidence...)
	case SubjectKindFinding:
		var payload frontier.FindingPayload
		if err := json.Unmarshal(published.Payload, &payload); err != nil {
			return Artifact{}, fmt.Errorf("decode remote finding: %w", err)
		}
		artifact.Title = summarizeLine(payload.Title)
		artifact.Evidence = append([]frontier.Evidence{}, payload.CounterEvidence...)
	case SubjectKindProposal:
		var payload frontier.ProposalPayload
		if err := json.Unmarshal(published.Payload, &payload); err != nil {
			return Artifact{}, fmt.Errorf("decode remote proposal: %w", err)
		}
		artifact.Title = summarizeLine(payload.Title)
		artifact.Evidence = append(append([]frontier.Evidence{}, payload.Supporting...),
			payload.Conflicting...)
		artifact.Criteria = suggestedCriteria(published.ID, payload.VerificationCriteria)
		// A remote proposal's form is the same derivation as a local
		// one's: it rests on findings or it does not. RestsOn is what
		// the producer asserted, so nothing here infers authority a
		// candidate remedy does not have.
		artifact.Status = string(frontier.ProposalCandidate)
		for _, rests := range published.RestsOn {
			if rests.Kind == frontier.EntityFinding {
				artifact.Status = string(frontier.ProposalConsolidated)
			}
		}
	}
	for _, rests := range published.RestsOn {
		kind, ok := subjectKindOfEntity(rests.Kind)
		if !ok {
			continue
		}
		artifact.Related = append(artifact.Related, Subject{Kind: kind, ID: rests.ID})
	}
	return artifact, nil
}

// decodeAnswer reads one remote operator review decision.
func decodeAnswer(published frontier.PublishedRecord) *remoteAnswer {
	if published.Answer == nil {
		return nil
	}
	kind, ok := subjectKindOfEntity(published.Subject.Type)
	if !ok {
		return nil
	}
	return &remoteAnswer{
		subject:   Subject{Kind: kind, ID: published.Subject.ID},
		decision:  string(published.Answer.Decision),
		refine:    published.Answer.Decision == "",
		createdAt: published.CreatedAt,
	}
}

// applyRemoteReviewStatus derives review status for artifacts that have none.
//
// It mirrors internal/frontier's derivation exactly — newest decision wins, and
// a rejection with a refinement request beside it is `refine-requested` — and it
// only ever fills in a status that is empty. A local artifact already carries
// the store's own derivation, which is authoritative, so this never overwrites
// one; and historical triage advice is not a disposition, so nothing here can
// promote advice into a review state.
func applyRemoteReviewStatus(artifacts []Artifact, answers []remoteAnswer) {
	if len(answers) == 0 {
		return
	}
	type decision struct {
		at       time.Time
		decision string
		refine   bool
	}
	newest := make(map[Subject]decision, len(answers))
	refinements := make(map[Subject]bool, len(answers))
	for _, answer := range answers {
		if answer.refine {
			refinements[answer.subject] = true
			continue
		}
		if current, ok := newest[answer.subject]; ok && !answer.createdAt.After(current.at) {
			continue
		}
		newest[answer.subject] = decision{at: answer.createdAt, decision: answer.decision}
	}
	for i := range artifacts {
		if artifacts[i].ReviewStatus != "" {
			continue
		}
		found, ok := newest[artifacts[i].Subject]
		if !ok {
			continue
		}
		switch frontier.Disposition(found.decision) {
		case frontier.DispositionAccept:
			artifacts[i].ReviewStatus = string(frontier.ReviewAccepted)
		case frontier.DispositionReject:
			if refinements[artifacts[i].Subject] {
				artifacts[i].ReviewStatus = string(frontier.ReviewRefineRequested)
			} else {
				artifacts[i].ReviewStatus = string(frontier.ReviewRejected)
			}
		case frontier.DispositionDefer:
			artifacts[i].ReviewStatus = string(frontier.ReviewDeferred)
		case frontier.DispositionDuplicate:
			artifacts[i].ReviewStatus = string(frontier.ReviewDuplicate)
		}
	}
}

// ledgerView is one batch's read of the Reality ledger.
//
// It is assembled once per Artifacts call because its two expensive reads — the
// unresolved questions and each entity's current facts — are shared by every
// artifact in the batch, and because §E5 requires one instant to serve the
// whole read: a context assembled from facts read at two different moments
// could report a work allowance the ledger never held simultaneously.
type ledgerView struct {
	source *babelSource
	// attention is the versioned focus consultation. It is nil when no
	// ledger is open, which internal/reality accepts by design.
	attention *reality.Attention
	// questionsByEntity counts the unresolved questions targeting each
	// entity. An unresolved question is recorded, attributed friction about
	// a subject — §4.8 asks one only when an answer would change what
	// analysis does — which is why it is the pain signal rather than
	// anything derived from what an operator clicked.
	questionsByEntity map[string]int
	// facts caches each entity's current facts by predicate.
	facts map[string]map[reality.Predicate]reality.Fact
	// complaints is the deployment's count of head operator complaints,
	// which raises recorded pain for every subject rather than being
	// attributed to one. A complaint names no entity, and guessing which
	// artifact a sentence was about is the inference this package refuses.
	complaints int
	// unavailable is an outage: something that should have answered did not.
	// It propagates to SourceStatus and therefore to Page.Unavailable.
	unavailable string
	// notes are structural absences rather than failures: a store this build
	// was not given, on a machine where that is a legitimate configuration.
	// They reach each artifact's Context.Unknown, where a reader can see
	// exactly what was not consulted, and deliberately NOT SourceStatus — a
	// supported configuration must not report itself as degraded, or every
	// page would carry an unavailability notice that never clears.
	notes string
	// version identifies the ledger state this view read, so two contexts
	// built from it carry the same provenance.
	version string
}

// openLedger assembles the batch's ledger read.
func (s *babelSource) openLedger(ctx context.Context, asOf time.Time) (*ledgerView, error) {
	view := &ledgerView{
		source:            s,
		questionsByEntity: make(map[string]int),
		facts:             make(map[string]map[reality.Predicate]reality.Fact),
	}
	if s.complaints != nil {
		heads, err := s.complaints.Heads(ctx)
		if err != nil {
			view.unavailable = joinReasons(view.unavailable,
				fmt.Sprintf("operator complaints unreadable: %v", err))
		} else {
			view.complaints = len(heads)
		}
	} else {
		view.notes = joinReasons(view.notes,
			"no complaint store is wired on this instance, so recorded operator pain is not counted")
	}
	if s.ledger == nil {
		// A machine with no Reality ledger is a supported configuration, not
		// a degraded read: the absence is stated per artifact as unknown
		// context, and calling it an outage would make every page on such a
		// host claim its tally might be incomplete.
		view.notes = joinReasons(view.notes,
			"no Reality ledger is open on this instance, so recorded work, pain and allowances are unavailable")
		view.version = "ledger-absent"
		return view, nil
	}
	view.attention = reality.NewAttention(s.ledger, s.focusVersion)

	listings, err := s.ledger.Questions(ctx, reality.QuestionQuery{States: unresolvedQuestionStates()})
	if err != nil {
		view.unavailable = joinReasons(view.unavailable,
			fmt.Sprintf("Reality questions unreadable: %v", err))
	}
	newest := asOf
	for _, listing := range listings {
		for _, entity := range listing.Question.TargetEntityIDs {
			view.questionsByEntity[entity]++
		}
		if listing.Question.CreatedAt.After(newest) {
			newest = listing.Question.CreatedAt
		}
	}
	view.version = strconv.Itoa(s.focusVersion) + ":" + strconv.Itoa(len(listings))
	return view, nil
}

// unresolvedQuestionStates are the question states that still represent an open
// need.
//
// Snoozed is among them and declined is not, and the difference is §4.8's:
// snoozing defers without answering, so the need survives, while declining is a
// recorded refusal to answer and suppresses the ask. Counting a declined
// question as pain would turn an operator's "no" into a reason to keep pushing.
func unresolvedQuestionStates() []reality.QuestionState {
	return []reality.QuestionState{
		reality.QuestionOpen,
		reality.QuestionAnsweredUninterpreted,
		reality.QuestionInterpreting,
		reality.QuestionPlanReady,
		reality.QuestionSnoozed,
	}
}

// contextFor assembles one subject's recorded context.
//
// Everything in the result is read from a record. Priority is derived from the
// ledger's own lifecycle and ownership facts under a stated rule, not from the
// model's novelty and priority estimates — those are content-derived scores
// that §5.2 confines to ordering the frontier and that §E3 keeps out of a
// blinded read entirely, so treating one as the operator's priority would let
// the corpus rank itself.
//
// Missing and conflicting context stays explicit. An unresolved name, an
// ambiguous one, an uninstalled focus policy, and an absent lifecycle or
// ownership fact each append an Unknown entry, because §E5 requires the gap to
// be visible rather than defaulted.
//
// filed are §4.13's topics: the entities this record is filed under, which are
// subjects of this context exactly as a resolved name is. Nothing downstream
// distinguishes them, and that is the point — the operator's stance toward a
// topic is lifecycle and analysis-policy facts, so a record filed under a
// paused one draws under the allowance those facts produce whether the pause
// was stated about a word the run happened to write down or about the topic
// the record was filed under.
func (v *ledgerView) contextFor(ctx context.Context, names, filed []string,
	asOf time.Time) (Context, error) {
	out := Context{Version: "", Allowance: string(reality.AllowanceFull)}
	for _, note := range []string{v.unavailable, v.notes} {
		if note != "" {
			out.Unknown = append(out.Unknown, note)
		}
	}
	if len(names) == 0 && len(filed) == 0 {
		out.Unknown = append(out.Unknown,
			"this record carries no recorded label, scope or target and is filed under no topic, "+
				"so no Reality subject could be consulted")
	}
	// A complaint the operator recorded is pain the deployment holds, but it
	// names no entity: matching its text against a model-authored title
	// would be this package inventing the link the ledger exists to own. So
	// it is reported as an unattributed deployment-level signal in Reasons
	// and deliberately does NOT raise this subject's Pain — a number that
	// rose identically for every artifact would rank nothing differently
	// while claiming an operator fact about each one.
	if v.complaints > 0 {
		out.Reasons = append(out.Reasons, fmt.Sprintf(
			"%d recorded operator complaint(s) stand unaddressed in this deployment; "+
				"none is linked to this record, so none raises its recorded pain", v.complaints))
	}
	if v.attention == nil {
		out.Version = contextDigest(out, v.version)
		return out, nil
	}

	admission, err := v.attention.Admit(ctx, reality.AdmitRequest{
		Names:     names,
		EntityIDs: filed,
		Work:      reality.WorkSubjectSpecific,
		AsOf:      asOf,
	})
	if err != nil {
		return Context{}, fmt.Errorf("evaluation: consult Reality attention: %w", err)
	}
	out.Allowance = string(admission.Allowance)
	out.Blocked = !admission.Permitted
	if admission.Policy == 0 {
		out.Unknown = append(out.Unknown,
			"no Reality focus policy version is installed, so no operator expenditure decision applies")
	} else {
		out.Reasons = append(out.Reasons, admission.Reason())
	}
	if admission.Unresolved > 0 {
		out.Unknown = append(out.Unknown, fmt.Sprintf(
			"%d recorded name(s) or filed topic(s) resolve to no Reality entity", admission.Unresolved))
	}
	if admission.Ambiguous > 0 {
		out.Unknown = append(out.Unknown, fmt.Sprintf(
			"%d recorded name(s) resolve to several Reality entities and decide nothing", admission.Ambiguous))
	}
	if admission.Contested {
		out.Unknown = append(out.Unknown,
			"a fact behind this expenditure decision is stale or disputed")
	}

	for _, entity := range admission.Subjects {
		facts, err := v.currentFacts(ctx, entity, asOf)
		if err != nil {
			return Context{}, err
		}
		v.applyFacts(&out, entity, facts)
		if open := v.questionsByEntity[entity]; open > 0 {
			out.Pain += open
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"%d unresolved Reality question(s) target %s", open, entity))
		}
	}
	if len(filed) > 0 {
		out.Reasons = append(out.Reasons, fmt.Sprintf(
			"this record is filed under %d Reality topic(s), whose recorded facts apply to it", len(filed)))
	}
	if len(admission.Subjects) == 0 && (len(names) > 0 || len(filed) > 0) {
		out.Unknown = append(out.Unknown,
			"none of this record's recorded names or topics resolve to a Reality subject, "+
				"so no work allowance applies")
	}
	sort.Strings(out.Unknown)
	out.Version = contextDigest(out, v.version)
	return out, nil
}

// applyFacts folds one entity's current facts into the context.
//
// The mapping is stated rather than inferred, and it is deliberately coarse.
// Lifecycle and ownership are operator intent, so an active project the
// operator owns is current work and a retired one is not; a deployed service is
// live and therefore carries more consequence than an undeployed one. Nothing
// here reaches a work allowance — §4.8 requires a versioned focus rule to do
// that, and Admit already did.
func (v *ledgerView) applyFacts(out *Context, entity string, facts map[reality.Predicate]reality.Fact) {
	lifecycle, hasLifecycle := facts[reality.PredicateLifecycle]
	ownership, hasOwnership := facts[reality.PredicateOwnership]
	if !hasLifecycle {
		out.Unknown = append(out.Unknown, fmt.Sprintf("%s has no recorded lifecycle", entity))
	}
	if !hasOwnership {
		out.Unknown = append(out.Unknown, fmt.Sprintf("%s has no recorded ownership", entity))
	}
	owned := hasOwnership && (ownership.Value.Enum == reality.OwnershipOwned ||
		ownership.Value.Enum == reality.OwnershipContributed)
	if hasLifecycle && lifecycle.Value.Enum == reality.LifecycleActive && owned {
		out.CurrentWork = true
		out.Priority += 2
		out.Reasons = append(out.Reasons, fmt.Sprintf(
			"%s is recorded as active work the operator %s", entity, ownership.Value.Enum))
	}
	if hasOwnership && ownership.Value.Enum == reality.OwnershipOwned {
		out.Priority++
	}
	if hasLifecycle {
		switch lifecycle.Value.Enum {
		case reality.LifecycleDormant, reality.LifecycleRetired:
			out.Priority--
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"%s is recorded as %s", entity, lifecycle.Value.Enum))
		}
	}
	if state, ok := facts[reality.PredicateDeploymentState]; ok &&
		state.Value.Enum == reality.DeploymentDeployed {
		out.Priority++
		out.Reasons = append(out.Reasons, fmt.Sprintf("%s is recorded as deployed", entity))
	}
	for _, fact := range facts {
		switch fact.Status {
		case reality.FactDisputed:
			out.Pain++
			out.Unknown = append(out.Unknown, fmt.Sprintf(
				"%s's %s fact is disputed", entity, fact.Predicate))
		case reality.FactStale:
			out.Unknown = append(out.Unknown, fmt.Sprintf(
				"%s's %s fact is stale", entity, fact.Predicate))
		}
		if fact.Payload.Provenance == nil {
			continue
		}
		note := fact.Payload.Note
		if note == "" {
			note = fmt.Sprintf("recorded %s for %s", fact.Predicate, entity)
		}
		evidence, err := frontier.NewEvidence(*fact.Payload.Provenance, note)
		if err != nil {
			continue
		}
		out.Evidence = append(out.Evidence, evidence)
	}
}

// currentFacts reads one entity's newest in-force fact per predicate.
//
// Newest by observed time, then recorded time, then id — internal/reality's own
// ranking, restated here because Facts returns revisions rather than a current
// view and the alternative would be a second answer to "what does the ledger
// say now". Superseded revisions are excluded by the query; proposed, active,
// disputed and stale ones are kept, because a disputed fact is context an
// operator needs to see rather than a fact to hide.
func (v *ledgerView) currentFacts(ctx context.Context, entity string,
	asOf time.Time) (map[reality.Predicate]reality.Fact, error) {
	if cached, ok := v.facts[entity]; ok {
		return cached, nil
	}
	facts, err := v.source.ledger.Facts(ctx, reality.FactQuery{
		SubjectID: entity,
		AsOf:      asOf,
		Statuses: []reality.FactStatus{
			reality.FactActive, reality.FactProposed,
			reality.FactDisputed, reality.FactStale,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: read Reality facts for %s: %w", entity, err)
	}
	current := make(map[reality.Predicate]reality.Fact, len(facts))
	for _, fact := range facts {
		incumbent, ok := current[fact.Predicate]
		if !ok || newerFact(fact, incumbent) {
			current[fact.Predicate] = fact
		}
	}
	v.facts[entity] = current
	return current, nil
}

// newerFact ranks two facts for "which one is current".
func newerFact(candidate, incumbent reality.Fact) bool {
	if !candidate.ObservedAt.Equal(incumbent.ObservedAt) {
		return candidate.ObservedAt.After(incumbent.ObservedAt)
	}
	if !candidate.RecordedAt.Equal(incumbent.RecordedAt) {
		return candidate.RecordedAt.After(incumbent.RecordedAt)
	}
	return candidate.ID > incumbent.ID
}

// contextDigest is the context's material version.
//
// It covers exactly what would change a recommendation or make a review due
// again: the recorded priority, current-work and pain readings, the blocked
// flag, the work allowance, the unknowns, and the ledger state the whole view
// was read at. It deliberately covers none of evaluation's own bookkeeping — no
// vote counts, no assignments, no attempts, no checkpoints — because those are
// published on every sweep, and folding them in would make each sweep report
// every artifact's context as changed and restore cooled-down items as fresh
// reality.
//
// Reasons and Evidence are excluded for the same reason from the other side:
// they are prose and locators that explain the readings above, and a reworded
// explanation is not a changed allowance.
func contextDigest(out Context, ledgerVersion string) string {
	parts := []string{
		ledgerVersion,
		strconv.Itoa(out.Priority),
		strconv.FormatBool(out.CurrentWork),
		strconv.Itoa(out.Pain),
		strconv.FormatBool(out.Blocked),
		out.Allowance,
	}
	parts = append(parts, out.Unknown...)
	return "ctx-" + digestOf(parts...)
}

// suggestedCriteria turns a proposal's own verification criteria into
// identified criteria.
//
// The identifier is derived from the proposal and the criterion's text, so the
// same suggestion keeps the same id across refreshes and two different
// suggestions never collide. It is a suggestion throughout: an outcome
// assessment records the criteria version it judged against, and §E6's rule
// that Babel cannot rewrite its target and verify itself against the
// replacement is what makes the operator's criteria record — not this — the
// authority.
func suggestedCriteria(subjectID string, texts []string) []Criterion {
	if len(texts) == 0 {
		return nil
	}
	out := make([]Criterion, 0, len(texts))
	for _, text := range texts {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		out = append(out, Criterion{
			ID:          "crit-" + digestOf(subjectID, trimmed),
			Description: trimmed,
		})
	}
	return out
}

// subjectKindOf maps a retrieval output kind to a subject kind.
func subjectKindOf(kind frontier.OutputKind) (string, bool) {
	switch kind {
	case frontier.OutputHypothesis:
		return SubjectKindHypothesis, true
	case frontier.OutputObservation:
		return SubjectKindObservation, true
	case frontier.OutputFinding:
		return SubjectKindFinding, true
	}
	return "", false
}

// subjectKindOfEntity maps a frontier entity kind to a subject kind.
func subjectKindOfEntity(kind frontier.EntityType) (string, bool) {
	switch kind {
	case frontier.EntityHypothesis:
		return SubjectKindHypothesis, true
	case frontier.EntityObservation:
		return SubjectKindObservation, true
	case frontier.EntityFinding:
		return SubjectKindFinding, true
	case frontier.EntityProposal:
		return SubjectKindProposal, true
	}
	return "", false
}

// publishedSubjectKind maps a wire record kind to a subject kind.
func publishedSubjectKind(kind frontier.PublishedKind) (string, bool) {
	switch kind {
	case frontier.PublishedHypothesis:
		return SubjectKindHypothesis, true
	case frontier.PublishedObservation:
		return SubjectKindObservation, true
	case frontier.PublishedFinding:
		return SubjectKindFinding, true
	case frontier.PublishedProposal:
		return SubjectKindProposal, true
	}
	return "", false
}

// entityTypeOf maps a subject kind to the frontier entity it names.
//
// The switch is written out rather than cast, even though the strings agree, so
// that a subject kind this package added — "evaluation" — cannot be handed to a
// store that has no table for it.
func entityTypeOf(kind string) (frontier.EntityType, bool) {
	switch kind {
	case SubjectKindHypothesis:
		return frontier.EntityHypothesis, true
	case SubjectKindObservation:
		return frontier.EntityObservation, true
	case SubjectKindFinding:
		return frontier.EntityFinding, true
	case SubjectKindProposal:
		return frontier.EntityProposal, true
	}
	return "", false
}

// reviewableEntity reports whether §6.7 admits an operator disposition against
// this record kind, which is the only question review status answers.
//
// Observations are excluded, and the exclusion is internal/frontier's rather
// than this package's: an observation is the evidence a finding consolidates,
// not an artifact an operator accepts or rejects. Asking the store for one's
// review status would get `new` back and render every observation as awaiting a
// decision nobody can make.
func reviewableEntity(entity frontier.EntityType) bool {
	switch entity {
	case frontier.EntityHypothesis, frontier.EntityFinding, frontier.EntityProposal:
		return true
	}
	return false
}

// wrapRead turns a store read failure into this package's vocabulary.
func wrapRead(err error, subject Subject) error {
	if errors.Is(err, frontier.ErrUnknownEntity) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, subject.Kind, subject.ID)
	}
	return fmt.Errorf("evaluation: read %s %s: %w", subject.Kind, subject.ID, err)
}

// sortArtifacts puts the inventory in a total order.
//
// Creation then kind then id, and deliberately not any score: this is the
// enumeration a projection is built from, and §5.4's rule that a list position
// must not read as strength applies before any ranking has happened.
func sortArtifacts(artifacts []Artifact) {
	sort.Slice(artifacts, func(i, j int) bool {
		if !artifacts[i].CreatedAt.Equal(artifacts[j].CreatedAt) {
			return artifacts[i].CreatedAt.Before(artifacts[j].CreatedAt)
		}
		if artifacts[i].Subject.Kind != artifacts[j].Subject.Kind {
			return artifacts[i].Subject.Kind < artifacts[j].Subject.Kind
		}
		return artifacts[i].Subject.ID < artifacts[j].Subject.ID
	})
}

// joinReasons appends one degradation sentence to another, keeping both.
//
// Both, because two different outages call for two different responses and a
// last-writer-wins field would send an operator to fix the object store while
// the ledger was the problem.
func joinReasons(existing, added string) string {
	switch {
	case added == "":
		return existing
	case existing == "":
		return added
	case strings.Contains(existing, added):
		return existing
	}
	return existing + "; " + added
}
