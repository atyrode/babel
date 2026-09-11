package web

// The detail routes' shared-catalog fallback (issue #208).
//
// The listings read the whole deployment by default (see fleetScope), and a
// detail route that resolved only against this machine's durable store
// answered 404 for rows those listings had just shown. To the operator that is
// not a scope subtlety: he clicked a record Babel had just told him about and
// Babel said there was no such record, which reads as lost work rather than as
// a store boundary.
//
// So a detail lookup that misses locally asks the catalog the listings read,
// by record id, and renders what comes back through the projections those
// listings already use. Four rules hold for every route that does it.
//
// Only an absence falls back. internal/frontier reports a record it does not
// hold with ErrUnknownEntity and internal/review with ErrUnknownRecord, and
// those two sentinels are the whole condition. Every other local failure is a
// durable store that broke, and a fallback that ran on one would answer a
// failed read with a 404 — or, worse, with a record fetched from somewhere
// else — turning a repairable fault into an invisible one.
//
// `?fleet=0` switches it off. That narrowing is the one question on this
// surface genuinely about this machine, and answering it with a record this
// machine does not hold would leave an operator standing in front of it no way
// to ask.
//
// A catalog-resolved record carries what the catalog holds and nothing more.
// The review status, the decision history, the observations a consolidation
// was reached through and a candidate's status history are this machine's own
// derivations over records it holds; rendering their zero values would say that
// a record nobody here has read has been decided nothing, which is the
// substitution the merged listings already refuse (see fleetQueueItem). They
// are absent, and the envelope says absent rather than null.
//
// Degrading is in record terms. A catalog that did not answer leaves this
// surface unable to say whether the record exists at all, and that is what the
// page is told: this record could not be read right now. Which machine
// published it and whether it has committed are not part of that sentence,
// because the reader asked about a record and neither of those is an answer to
// what he asked.

import (
	"encoding/json"
	"net/http"

	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// recordUnreadable is what a detail route says when the catalog it fell back to
// did not answer.
//
// It is a sentence about the record rather than about the deployment, for the
// reason this file's header gives, and it is a fixed sentence rather than the
// error's own text for serviceError's: a wrapped catalog error can carry a
// connection string.
const recordUnreadable = "this record could not be read right now, " +
	"because the shared catalog did not answer"

// recordUnopened is what a detail route says when the catalog held the record
// and this build could not open it.
//
// internal/fleet's own reason is not repeated, and that is deliberate beyond
// recordUnreadable's caution: that reason names the key a record is sealed
// under, which is custody diagnostics for an operator at a terminal rather
// than something a page about one finding should be explaining to its reader.
const recordUnopened = "this record's content could not be opened by this build"

// recordNotice is the notice a detail route carries when it has no record to
// render.
func recordNotice(detail string) syncNotice {
	return syncNotice{SyncDegraded: true, SyncDetail: detail}
}

// catalogLookup is what a detail route's fallback found.
type catalogLookup struct {
	// record is the catalog's copy of the record, valid only when found. It
	// always carries a Published projection then, because a record that could
	// not be opened is reported through notice instead: a projection every
	// caller would have to nil-check is a projection one caller eventually
	// does not.
	record fleet.Record
	found  bool
	// notice says this surface could not read the record — the catalog did not
	// answer, or it answered with an object this build cannot open. A route
	// carries it rather than answering 404, because "the deployment holds no
	// such record" and "I could not look" are different answers and only one
	// of them is about the record.
	notice syncNotice
}

// answerable reports whether the fallback has something to answer with: the
// record, or the notice saying it could not be read. Neither of those leaves
// the route's own local absence standing, which is what a route reports when
// this is false.
func (l catalogLookup) answerable() bool { return l.found || l.notice.SyncDegraded }

// catalogRecord resolves one record by id from the catalog the listings read.
//
// wide is the caller's already-resolved fleet scope, so `?fleet=0` and a
// machine with no shared backend both skip the read and leave the local
// absence standing. It is the caller's rather than this function's because a
// detail route must refuse a malformed `?fleet=` before it reads anything,
// exactly as the listings do.
//
// The read is kind-narrowed and committed-only, which are what make it the
// same record the listing showed: the kind is what stops /api/finding
// answering with a proposal that shares an identifier, and SPEC.md §9 keeps
// staged output out of every fleet-wide listing, so every row an operator can
// click from one has committed.
func (s *Server) catalogRecord(r *http.Request, wide bool, id string,
	kind sharedcatalog.RecordKind) catalogLookup {
	if !wide {
		return catalogLookup{}
	}
	err := s.opts.FleetError
	var records []fleet.Record
	if err == nil {
		records, err = s.opts.Fleet.RecordsWithContent(r.Context(), sharedcatalog.RecordFilter{
			RecordIDs: []string{id},
			Kinds:     []sharedcatalog.RecordKind{kind},
			Limit:     1,
		})
	}
	switch {
	case fleet.NotConfigured(err):
		// No shared backend is no second place to look, which is the local
		// absence rather than a failure to report.
		return catalogLookup{}
	case err != nil:
		s.logf("%s %s degraded: %s", r.Method, r.URL.Path, recordUnreadable)
		return catalogLookup{notice: recordNotice(recordUnreadable)}
	case len(records) == 0:
		return catalogLookup{}
	case records[0].Published == nil:
		// A record this instance holds no key for, or one whose object the
		// store refused. It exists and cannot be shown, which is neither a
		// 404 nor a page of empty fields with nothing saying why.
		return catalogLookup{notice: recordNotice(recordUnopened)}
	}
	return catalogLookup{record: records[0], found: true}
}

// catalogKind names a record's kind in the shared catalog's vocabulary.
//
// It is a switch rather than a conversion between two string types that
// currently agree: neither package promises the other that they will keep
// agreeing, and a conversion that produced a kind the catalog does not know
// would narrow the read to nothing and read as a deployment that holds no such
// record.
func catalogKind(kind frontier.EntityType) (sharedcatalog.RecordKind, bool) {
	switch kind {
	case frontier.EntityHypothesis:
		return sharedcatalog.KindHypothesis, true
	case frontier.EntityObservation:
		return sharedcatalog.KindObservation, true
	case frontier.EntityFinding:
		return sharedcatalog.KindFinding, true
	case frontier.EntityProposal:
		return sharedcatalog.KindProposal, true
	}
	return "", false
}

// catalogHypothesis renders a catalog-resolved candidate on /api/hypothesis's
// own shape, or the notice saying it could not be read.
//
// The statement comes from the published payload rather than from
// fleet.Record.Summary(), and that is the one place a detail route parts
// company with a listing row: a row shows one bounded line, and a page a
// reader opened in order to read the claim shows the claim. The bytes are the
// producing store's own payload_json carried verbatim through the sealed
// envelope, so what renders here is the document that store would have handed
// back.
//
// id is the route's parameter rather than the record's, because in the
// unreadable case there is no record to take an identifier from and a page
// that could not name the record it failed on would be asking its reader which
// record he had clicked.
func catalogHypothesis(id string, found catalogLookup) hypothesisDetail {
	detail := hypothesisDetail{
		syncNotice:    found.notice,
		Hypothesis:    hypothesisView{ID: id},
		StatusHistory: []statusEventView{},
		Observations:  []observationView{},
		Links:         []linkView{},
		Proposals:     []proposalView{},
		Lineage: viewLineage(review.Lineage{
			Node: review.Node{Kind: review.KindHypothesis, ID: id},
		}),
	}
	if !found.found {
		return detail
	}
	published := found.record.Published
	var payload frontier.HypothesisPayload
	if err := json.Unmarshal(published.Payload, &payload); err != nil {
		detail.syncNotice = recordNotice(recordUnopened)
		return detail
	}
	detail.Hypothesis = hypothesisView{
		ID:            published.ID,
		AncestorID:    published.Ancestor,
		RunID:         published.RunID,
		SchemaVersion: published.Schema,
		CreatedAt:     timeText(published.CreatedAt),
		// Status is the lifecycle state the record carried when it was staged
		// for publication, which fleetHypothesis documents as a snapshot: the
		// history that would move it is append-only on the store that holds
		// it. ReviewStatus stays absent, on this file's third rule.
		Status:  string(published.Status),
		Payload: payload,
	}
	return detail
}

// catalogFinding renders a catalog-resolved consolidation on /api/finding's
// own shape, or the notice saying it could not be read.
func catalogFinding(id string, found catalogLookup) findingDetail {
	detail := findingDetail{
		syncNotice: found.notice,
		Finding: findingView{
			ID:             id,
			ObservationIDs: []string{},
			HypothesisIDs:  []string{},
		},
		Observations: []observationView{},
		Proposals:    []proposalView{},
	}
	if !found.found {
		return detail
	}
	published := found.record.Published
	var payload frontier.FindingPayload
	if err := json.Unmarshal(published.Payload, &payload); err != nil {
		detail.syncNotice = recordNotice(recordUnopened)
		return detail
	}
	detail.Finding = findingView{
		ID:            published.ID,
		AncestorID:    published.Ancestor,
		RunID:         published.RunID,
		SchemaVersion: published.Schema,
		CreatedAt:     timeText(published.CreatedAt),
		// The observations and candidates a consolidation was reached through
		// are relationships the published envelope does not carry —
		// frontier.PublishedRecord.RestsOn belongs to a proposal — so they are
		// empty rather than reconstructed from the payload's prose, which
		// names no identifiers and could only be guessed from.
		ObservationIDs: []string{},
		HypothesisIDs:  []string{},
		Payload:        payload,
	}
	return detail
}

// catalogProposal renders a catalog-resolved proposal on /api/proposals/{id}'s
// own shape, or the notice saying it could not be read.
//
// The row half is fleetProposal's, which is the same projection the proposals
// listing renders: the title is internal/fleet's one bounded line, so a
// proposal reads the same in the row an operator clicked and on the page it
// opened. The four payload fields that listing deliberately leaves empty are
// filled here, because this route opened exactly one record and the whole
// document is what a reader came for.
func (s *Server) catalogProposal(id string, found catalogLookup) proposalDetail {
	detail := proposalDetail{
		syncNotice:      found.notice,
		ProposalSummary: ProposalSummary{ID: id},
		FindingIDs:      []string{},
		HypothesisIDs:   []string{},
	}
	if !found.found {
		return detail
	}
	published := found.record.Published
	var payload frontier.ProposalPayload
	if err := json.Unmarshal(published.Payload, &payload); err != nil {
		detail.syncNotice = recordNotice(recordUnopened)
		return detail
	}
	detail.ProposalSummary = fleetProposal(found.record, s.opts.Fleet.LocalHost())
	detail.Problem = payload.Problem
	detail.Outcome = payload.Outcome
	detail.Impact = string(payload.Impact)
	detail.Classification = string(payload.Classification)
	detail.AncestorID = published.Ancestor
	detail.SchemaVersion = published.Schema
	detail.Payload = payload
	detail.FindingIDs, detail.HypothesisIDs, detail.Form = publishedRestsOn(published)
	return detail
}

// publishedRestsOn reads what a published proposal rests on: the findings and
// the candidates it names, and the form those make it.
//
// The form comes off the subject kind rather than off a count, because the
// kind is what internal/frontier's publisher wrote — proposalRestsOn names
// findings for §4.5's consolidation and hypotheses for #114's candidate remedy
// — so reading it back cannot disagree with the form the producing store
// derived from its own rows.
func publishedRestsOn(published *frontier.PublishedRecord) (
	findings, hypotheses []string, form string) {
	findings, hypotheses = []string{}, []string{}
	resting := frontier.ProposalCandidate
	for _, subject := range published.RestsOn {
		switch subject.Kind {
		case frontier.EntityFinding:
			findings = append(findings, subject.ID)
			resting = frontier.ProposalConsolidated
		case frontier.EntityHypothesis:
			hypotheses = append(hypotheses, subject.ID)
		}
	}
	return findings, hypotheses, string(resting)
}

// catalogHistory is the review history of a record this machine's review store
// has never held: none of it.
//
// Empty is the honest answer and it is not the same answer as "nobody has
// decided this record". A disposition is appended where the reviewer sat, and
// this store holds no entry for a record it does not hold; the derived status
// is therefore absent rather than `new`, on fleetQueueItem's terms — a status
// rendered as a fact would say that a record somebody may have rejected twice
// is awaiting its first reading.
func catalogHistory(found catalogLookup) historyResult {
	return historyResult{
		syncNotice:  found.notice,
		Decisions:   []decisionView{},
		Refinements: []refinementView{},
	}
}
