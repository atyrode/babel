package frontier

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Triage advice is Babel's own reading of a proposal it wrote, recorded beside
// that proposal for the person who will rule on it.
//
// This file is the whole of the authority a triage pass holds, and it is
// deliberately a separate type rather than three more methods on *Store. A
// pass that can rank, cluster, argue against and re-propose is useful; a pass
// that can also accept or reject is a pass that has taken the operator's job,
// and the difference must not depend on anybody remembering which methods to
// call. *Triage holds its store in an unexported field and embeds nothing, so
// Decide, RejectAndRefine, SetStatus and DeferFrontier are unreachable from a
// triage pass by construction: there is no assertion, no conversion and no
// embedded promotion that recovers them.
//
// What the advice may say is bounded by the same reasoning. A rank is a
// reading order among peers, not a verdict, and §5.4's rule that rank is not
// strength holds here as it does for a duplicate warning's overlap. The
// cluster says which records to compare, never which to keep. The
// counter-argument is required rather than optional, because advice that only
// argues for something is advocacy and the operator already has one of those
// in the proposal itself. And the alternative is a new proposal record: the
// original is left exactly as it was, still in the queue, still reviewable on
// its own terms, and the operator chooses between two records rather than
// discovering that one of them was rewritten.

// TriageAdvicePayload is the §9 encryption-bound part of one piece of triage
// advice.
//
// Everything here is derived from reading what records say, which is what
// decides that it travels sealed: §9's plaintext allowlist admits identifiers,
// counts, lifecycle state and timestamps, and migration 4 already settled that
// a judgement computed from two statements' content is none of those. A place
// in an ordering is such a judgement, so Rank sits here beside the prose
// rather than in a column an unkeyed reader could sort on.
type TriageAdvicePayload struct {
	// Rank is this proposal's place among the peers the pass triaged with
	// it, 1 being the one it suggests reading first. It is a suggested
	// reading order and nothing else; a proposal ranked last has not been
	// argued against, it has been put later in a queue.
	Rank int `json:"rank"`
	// Cohort is how many proposals the pass ranked in this pass, so a rank
	// can be read as a position rather than as a score. First of two and
	// first of forty are different claims.
	Cohort int `json:"cohort"`
	// Ranking is why this proposal earned that place, in the pass's own
	// words. It is optional: a rank with no argument is thin, but a rank
	// the pass cannot justify is better admitted than invented.
	Ranking string `json:"ranking,omitempty"`
	// CounterArgument is the case against acting on this proposal, which
	// the pass is required to make. A proposal arrives already argued for,
	// so the only thing a triage pass can add to the operator's reading of
	// it is the argument nobody else wrote down.
	CounterArgument string `json:"counter_argument"`
}

func (p TriageAdvicePayload) validate() error {
	if p.Rank < 1 {
		return fmt.Errorf("%w: triage rank %d is not a place in an ordering", ErrInvalidValue, p.Rank)
	}
	if p.Cohort < 1 {
		return fmt.Errorf("%w: triage cohort %d holds nothing", ErrInvalidValue, p.Cohort)
	}
	if p.Rank > p.Cohort {
		return fmt.Errorf("%w: triage rank %d of a cohort of %d", ErrInvalidValue, p.Rank, p.Cohort)
	}
	if p.CounterArgument == "" {
		return fmt.Errorf("%w: triage advice states no counter-argument", ErrInvalidValue)
	}
	return nil
}

// TriageAdvice is what one triage pass thought of one proposal before anybody
// ruled on it.
//
// It is never a decision, and the record has no room for one: no ruling, no
// reviewer, no place in a subject's review history. Several passes may each
// leave one, and each stays readable, because advice is a statement about the
// moment it was written rather than a state the next pass corrects.
type TriageAdvice struct {
	ID string
	// ProposalID is the record the advice is about.
	ProposalID string
	// AlternativeID is the proposal the pass offered instead, and is empty
	// when it offered none. The alternative is a record of its own, created
	// in the same transaction as this advice, resting on exactly what the
	// original rests on.
	AlternativeID string
	// Cluster names the peers the pass reads as saying the same thing,
	// strongest resemblance first as the pass ordered them. It is an
	// invitation to compare: §4.7's `duplicate` ruling is the operator's and
	// nothing here anticipates it.
	Cluster []string
	// RunID is the triage pass that wrote this.
	RunID      string
	RecordedAt time.Time
	Payload    TriageAdvicePayload
}

// TriageInput is one piece of advice a pass offers about one proposal.
type TriageInput struct {
	// ProposalID is the proposal being advised. It must be a stored
	// proposal on which no disposition has been recorded.
	ProposalID string
	// RunID is the triage pass. It is required for the reason every other
	// record's is: advice nobody can attribute to a run is advice whose
	// guidance and version cannot be looked up when it turns out to be
	// wrong.
	RunID string
	// Cluster names the peer proposals this one duplicates, in the pass's
	// own order. Each must be a stored proposal and none may be the subject;
	// a peer named twice is recorded once.
	Cluster []string
	Payload TriageAdvicePayload
}

// Triage is the authority a triage pass holds over the frontier: it may read
// the pile awaiting review, and it may attach advice and offer an alternative
// beside a record. It cannot rule on one.
//
// The guarantee is the type rather than a convention. *Triage embeds nothing
// and its store is unexported, so no caller holding one can reach Decide,
// RejectAndRefine, SetStatus or DeferFrontier, and no future method on *Store
// becomes reachable from here by being added.
type Triage struct {
	store *Store
}

// Triage narrows this store to what a triage pass may do with it.
//
// A caller that already holds the *Store obviously still holds it; this is not
// a sandbox and could not be one. What it is for is the boundary at the seam:
// the pass, and every function that assembles what the pass writes, takes
// *Triage, so the code that produces advice is code in which a disposition
// cannot be named.
func (s *Store) Triage() *Triage { return &Triage{store: s} }

// Pending reads the proposals awaiting a ruling: current wording, no
// disposition recorded, oldest first.
//
// Oldest first rather than by anything derived from content, because this is
// the pile the pass is asked to put in order and handing it a pre-sorted one
// would be answering the question in the retrieval. A zero or negative limit
// means DefaultListLimit, on the same terms as every other enumeration here.
func (t *Triage) Pending(ctx context.Context, limit int) ([]Proposal, error) {
	bounded, _ := ListFilter{Limit: limit}.bounds()
	ids, err := t.store.pageIDs(ctx, `SELECT p.id FROM frontier_proposal p
		WHERE NOT EXISTS(SELECT 1 FROM frontier_proposal d WHERE d.ancestor_id = p.id)
			AND NOT EXISTS(SELECT 1 FROM frontier_disposition e
				WHERE e.subject_type = 'proposal' AND e.subject_id = p.id)
		ORDER BY p.created_at, p.id LIMIT ? OFFSET ?`, bounded, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(ids))
	for _, id := range ids {
		record, err := t.store.Proposal(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// Advise attaches one piece of advice to a proposal and changes nothing about
// it.
func (t *Triage) Advise(ctx context.Context, in TriageInput) (TriageAdvice, error) {
	var advice TriageAdvice
	err := t.store.transact(ctx, func(tx *sql.Tx) error {
		var err error
		advice, err = t.store.appendTriageAdvice(ctx, tx, in, "")
		return err
	})
	if err != nil {
		return TriageAdvice{}, err
	}
	return advice, nil
}

// AdviseWithAlternative offers a better proposal instead, and the advice that
// says why, in one transaction.
//
// The two exist together or not at all, for the reason §4.7's rejection and
// refinement do: an alternative that reached the review queue without the
// advice explaining it would be a proposal the operator cannot account for,
// and advice naming an alternative that was never written would send a reader
// after a record that is not there.
//
// The alternative rests on exactly what the original rests on — the same
// findings for a consolidation, the same addressed claims for a candidate —
// and that is derived from the original's own rows rather than stated by the
// caller. Two things follow, both of them the point. The alternative has the
// original's form, so a triage pass cannot dress a rewording up as a
// consolidation that travelled a development path it never took. And the two
// records are peers in the queue: same support, same claims, different
// wording, and the operator picks.
func (t *Triage) AdviseWithAlternative(ctx context.Context, in TriageInput,
	alternative ProposalPayload) (TriageAdvice, Proposal, error) {
	var (
		advice TriageAdvice
		record Proposal
		actor  Actor
		pub    publication
	)
	err := t.store.transact(ctx, func(tx *sql.Tx) error {
		rests, err := restsOn(ctx, tx, in.ProposalID)
		if err != nil {
			return err
		}
		rests.runID = in.RunID
		rests.payload = alternative
		record, actor, pub, err = t.store.appendProposal(ctx, tx, rests)
		if err != nil {
			return err
		}
		advice, err = t.store.appendTriageAdvice(ctx, tx, in, record.ID)
		return err
	})
	if err != nil {
		return TriageAdvice{}, Proposal{}, err
	}
	if err := t.store.commit(ctx, pub); err != nil {
		return TriageAdvice{}, Proposal{}, err
	}
	t.store.mintProposalEdges(ctx, record, "", actor)
	return advice, record, nil
}

// restsOn reads what one proposal rests on, shaped as the write that would
// produce a peer of it.
//
// A proposal always rests on one or the other: CreateProposal refuses a
// consolidation with no finding and CreateCandidateProposal refuses a remedy
// addressing nothing. A proposal with neither is therefore a corrupted row
// rather than a third form, and it is named as one instead of silently
// producing an alternative that rests on nothing.
func restsOn(ctx context.Context, tx *sql.Tx, proposalID string) (proposalWrite, error) {
	if err := requireRow(ctx, tx, "frontier_proposal", proposalID); err != nil {
		return proposalWrite{}, fmt.Errorf("advised proposal: %w", err)
	}
	findings, err := queryIDs(ctx, tx, `SELECT finding_id FROM frontier_proposal_finding
		WHERE proposal_id = ? ORDER BY position`, proposalID)
	if err != nil {
		return proposalWrite{}, err
	}
	addressed, err := queryIDs(ctx, tx, `SELECT hypothesis_id FROM frontier_proposal_hypothesis
		WHERE proposal_id = ? ORDER BY position`, proposalID)
	if err != nil {
		return proposalWrite{}, err
	}
	if len(findings) == 0 && len(addressed) == 0 {
		return proposalWrite{}, fmt.Errorf("%w: proposal %s rests on nothing, so no alternative to it can",
			ErrInvalidValue, proposalID)
	}
	return proposalWrite{findingIDs: findings, addressed: addressed}, nil
}

// appendTriageAdvice writes the advice row and its cluster inside the caller's
// transaction.
//
// The ruling check is here rather than in a caller because it is the authority
// line's other half. The type system stops a triage pass writing a
// disposition; this stops it writing beside one. An operator who has already
// accepted or rejected a proposal has answered, and advice arriving afterwards
// would be rendered next to their ruling as though the question were still
// open.
func (s *Store) appendTriageAdvice(ctx context.Context, tx *sql.Tx,
	in TriageInput, alternativeID string) (TriageAdvice, error) {
	if in.RunID == "" {
		return TriageAdvice{}, fmt.Errorf("%w: triage advice run id is empty", ErrInvalidValue)
	}
	if err := in.Payload.validate(); err != nil {
		return TriageAdvice{}, err
	}
	if err := requireRow(ctx, tx, "frontier_proposal", in.ProposalID); err != nil {
		return TriageAdvice{}, fmt.Errorf("advised proposal: %w", err)
	}
	var ruled int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM frontier_disposition
		WHERE subject_type = ? AND subject_id = ?`,
		string(EntityProposal), in.ProposalID).Scan(&ruled); err != nil {
		return TriageAdvice{}, fmt.Errorf("read disposition history: %w", err)
	}
	if ruled > 0 {
		return TriageAdvice{}, fmt.Errorf("%w: proposal %s", ErrAlreadyRuled, in.ProposalID)
	}
	cluster, err := clusterOf(ctx, tx, in.ProposalID, in.Cluster)
	if err != nil {
		return TriageAdvice{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return TriageAdvice{}, err
	}
	id, err := newID("adv")
	if err != nil {
		return TriageAdvice{}, err
	}
	recorded := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_triage_advice(
		id, proposal_id, alternative_id, run_id, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		id, in.ProposalID, nullableID(alternativeID), in.RunID, formatTime(recorded), payload); err != nil {
		return TriageAdvice{}, fmt.Errorf("insert triage advice: %w", err)
	}
	for position, peer := range cluster {
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_triage_cluster(
			advice_id, proposal_id, position) VALUES(?, ?, ?)`, id, peer, position); err != nil {
			return TriageAdvice{}, fmt.Errorf("link triage cluster: %w", err)
		}
	}
	return TriageAdvice{
		ID:            id,
		ProposalID:    in.ProposalID,
		AlternativeID: alternativeID,
		Cluster:       cluster,
		RunID:         in.RunID,
		RecordedAt:    recorded,
		Payload:       in.Payload,
	}, nil
}

// clusterOf validates the peers a pass clustered the subject with, keeping the
// pass's own order and recording a peer named twice once.
//
// Each is checked against a stored proposal before the write is accepted, on
// #113's terms: a cluster pointing at a record nobody wrote would send an
// operator comparing against nothing, and the failure belongs at the write
// rather than at the read.
func clusterOf(ctx context.Context, tx *sql.Tx, subject string, peers []string) ([]string, error) {
	if len(peers) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(peers))
	out := make([]string, 0, len(peers))
	for _, peer := range peers {
		if peer == subject {
			return nil, fmt.Errorf("%w: a proposal cannot cluster with itself", ErrInvalidValue)
		}
		if _, repeated := seen[peer]; repeated {
			continue
		}
		seen[peer] = struct{}{}
		if err := requireRow(ctx, tx, "frontier_proposal", peer); err != nil {
			return nil, fmt.Errorf("clustered proposal: %w", err)
		}
		out = append(out, peer)
	}
	return out, nil
}

// TriageAdvice reads the advice a reader of one proposal should see.
//
// That is advice about the proposal and advice that offered it as somebody
// else's alternative, because one row is a statement about a pair: read from
// the original it says "here is the case against this, and here is what was
// offered instead", and read from the alternative it says "this was offered
// instead of that, for this reason". Returning only the first half would leave
// an alternative sitting in the queue with no account of where it came from.
//
// It is a read on *Store rather than on *Triage. Rendering advice to an
// operator is the reading surface's job, and the narrow write handle exists to
// bound what a pass may write, not to hide what it wrote.
func (s *Store) TriageAdvice(ctx context.Context, proposalID string) ([]TriageAdvice, error) {
	if proposalID == "" {
		return nil, fmt.Errorf("%w: proposal id is empty", ErrInvalidValue)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, proposal_id, alternative_id, run_id,
		recorded_at, payload_json FROM frontier_triage_advice
		WHERE proposal_id = ? OR alternative_id = ? ORDER BY recorded_at, id`,
		proposalID, proposalID)
	if err != nil {
		return nil, fmt.Errorf("read triage advice: %w", err)
	}
	defer rows.Close()
	var out []TriageAdvice
	for rows.Next() {
		var (
			record      TriageAdvice
			alternative sql.NullString
			recorded    string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &record.ProposalID, &alternative, &record.RunID,
			&recorded, &payload); err != nil {
			return nil, fmt.Errorf("read triage advice: %w", err)
		}
		record.AlternativeID = alternative.String
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("triage advice %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("decode triage advice %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read triage advice: %w", err)
	}
	for i := range out {
		if out[i].Cluster, err = queryIDs(ctx, s.db, `SELECT proposal_id FROM frontier_triage_cluster
			WHERE advice_id = ? ORDER BY position`, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TriageAdvised reports which of these proposals a triage pass has left advice
// about, so a listing can mark the rows that carry some.
//
// It answers presence and nothing else. A rank is the one field a listing
// could sort on, and a queue silently reordered by Babel's suggested reading
// order would be the pass doing the operator's triage rather than advising it,
// which is the line §5.2 draws when it confines a derived ordering to
// ordering. So the answer is a yes for each id and no number to sort by; a
// reader who wants the rank opens the record and reads the advice beside it.
//
// A proposal counts as advised when advice names it either way, matching what
// TriageAdvice returns for it. The two have to agree: a row marked in the
// listing that opened onto no advice, or an alternative that arrived unmarked
// and then explained itself, would each teach an operator to distrust the
// mark.
func (s *Store) TriageAdvised(ctx context.Context, proposalIDs []string) (map[string]bool, error) {
	if len(proposalIDs) == 0 {
		return map[string]bool{}, nil
	}
	placeholders := make([]string, len(proposalIDs))
	args := make([]any, 0, len(proposalIDs)*2)
	for i, id := range proposalIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	list := strings.Join(placeholders, ", ")
	for _, id := range proposalIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT proposal_id, alternative_id
		FROM frontier_triage_advice
		WHERE proposal_id IN (`+list+`) OR alternative_id IN (`+list+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read triage advice presence: %w", err)
	}
	defer rows.Close()
	wanted := make(map[string]struct{}, len(proposalIDs))
	for _, id := range proposalIDs {
		wanted[id] = struct{}{}
	}
	// Absent rather than false for a proposal nothing was said about: the
	// caller is asking which records carry advice, and a map of explicit
	// noes is a thing a renderer can accidentally show.
	out := make(map[string]bool, len(proposalIDs))
	for rows.Next() {
		var subject string
		var alternative sql.NullString
		if err := rows.Scan(&subject, &alternative); err != nil {
			return nil, fmt.Errorf("read triage advice presence: %w", err)
		}
		for _, id := range []string{subject, alternative.String} {
			if id == "" {
				continue
			}
			if _, ok := wanted[id]; ok {
				out[id] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read triage advice presence: %w", err)
	}
	return out, nil
}
