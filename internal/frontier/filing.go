package frontier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/reference"
)

// Filing is what a record is about (SPEC.md §4.13).
//
// A topic is a Reality Ledger entity and nothing else — a repository, a
// project, a machine, a service, a concept — so a filing is a link out of the
// analysis corpus into the ledger, carrying the rationale for the link and the
// author who asserted it. This package stores the link and refuses to have an
// opinion about the entity: entities are created by an attributed operator act
// (§4.8), and a filing naming an id the ledger does not hold is a caller's
// mistake rather than something to invent an entity for.
//
// Everything here is append-only, on the terms the rest of this package is.
// Re-filing supersedes, unfiling withdraws, and both are rows: §4.13 requires
// that "the history of where a record was filed and why is readable", which is
// only true if a correction leaves its predecessor byte-identical. Which
// filing is current is therefore derived rather than stored — the newest row
// for a record and a topic, live when that row is not a withdrawal — so no
// flag can come to disagree with the history behind it.

// FilingAuthor is who filed a record. Three authors, and the distinction is
// load-bearing rather than decorative: §4.13's triage recipe revisits what a
// heuristic filed, leaves what a run filed alone unless it has a reason, and
// never touches the operator's own filing.
type FilingAuthor string

// The authors a filing can have.
const (
	// FilingOperator is a person's own act, which is the only authority that
	// can also create the topic it files under (§4.8).
	FilingOperator FilingAuthor = "operator"
	// FilingRun is a model invocation Babel launched and receipted: the
	// triage recipe, or the run that wrote the record and named what it was
	// about in its structured result.
	FilingRun FilingAuthor = "run"
	// FilingHeuristic is Babel's own machinery with no model and no person
	// behind it — the repository-identity seeding §4.13 permits until the
	// recipe has run. It is never dressed as either of the others, because
	// the recipe reads exactly this to know what it still owes a judgement.
	FilingHeuristic FilingAuthor = "heuristic"
)

func (a FilingAuthor) valid() bool {
	switch a {
	case FilingOperator, FilingRun, FilingHeuristic:
		return true
	}
	return false
}

// ErrNotFiled reports an unfiling of a record that is not filed under that
// topic. It is distinguished from an unknown record because the two are
// different mistakes: one names a topic the record never had, and the other
// names a record this store does not hold.
var ErrNotFiled = errors.New("record is not filed under that topic")

// FilingInput files one record under one topic.
type FilingInput struct {
	Record   Ref
	EntityID string
	// Rationale is why this record belongs to this topic, in the filer's own
	// words. It is required: a filing with no reason is a claim about the
	// corpus that nobody can check, and §4.13 makes the rationale part of
	// what a filing is.
	Rationale string
	Author    FilingAuthor
	// AuthorID is the operator's identity or the run's id, and empty for a
	// heuristic — which is not an actor with a name and must not be given
	// one.
	AuthorID string
	// Heuristic marks a filing the triage recipe should revisit. It is
	// implied by FilingHeuristic and may also be set by a run that wants to
	// say its own filing was a guess.
	Heuristic bool
}

// Filing is one row of a record's filing history.
type Filing struct {
	ID     string
	Record Ref
	// EntityID is the topic, and empty for §4.13's other honest answer: a
	// record about nothing in particular, recorded with a reason rather than
	// left unfiled by omission.
	EntityID  string
	Rationale string
	Author    FilingAuthor
	AuthorID  string
	Heuristic bool
	// Withdrawn marks the row an unfiling appended. The filing it withdrew
	// keeps its own row and its rationale; this one carries the reason.
	Withdrawn      bool
	WithdrawReason string
	CreatedAt      time.Time
	// SupersedesID is the row this one replaced as the current answer for
	// this record and this topic, empty when there was none.
	SupersedesID string
}

// FilingPayload is the §9 encryption-bound half of a filing.
//
// Both fields are prose about the corpus — why this record belongs to this
// topic, and why that stopped being true — which §9's plaintext allowlist does
// not admit, on the same terms as a link's note. The author, its identity and
// the heuristic flag stay in columns because each is an identifier or a
// lifecycle bit.
type FilingPayload struct {
	Rationale      string `json:"rationale,omitempty"`
	WithdrawReason string `json:"withdraw_reason,omitempty"`
}

// EntityLifecycle answers whether the Reality Ledger has retired a topic.
//
// It is an interface and not an import of internal/reality because the
// dependency runs the other way — the ledger imports this package — and
// because a machine with no ledger open is a supported deployment: a nil
// lifecycle retires nothing, which is the honest answer for a store that
// cannot ask.
//
// §4.13 makes retirement re-queue a topic's filings for triage, and that is
// exactly one behaviour here: a filing under a retired entity is treated as
// absent by Unfiled and by EntitiesFiled. Nothing is deleted, the rows stay
// readable, and undoing the retirement brings the filings back with them.
type EntityLifecycle interface {
	EntityRetired(ctx context.Context, entityID string) (bool, error)
}

// WithEntities attaches the ledger read that tells this store which topics
// have been retired. Without it — the default — no topic is retired, which is
// what a frontier opened beside no ledger can truthfully say.
func WithEntities(l EntityLifecycle) Option {
	return func(s *Store) { s.entities = l }
}

// UseEntities hands an already-open store the ledger it consults, for the one
// launch order in which the frontier opens before the ledger does: the web
// server opens its analysis state first and the Reality Ledger after it.
func (s *Store) UseEntities(l EntityLifecycle) { s.entities = l }

// File records that a record is about a topic.
//
// A live filing of the same record and topic is superseded rather than
// duplicated: re-filing with a better rationale is a correction, and two rows
// both claiming to be the current filing would make "why is this here" a
// question with two answers.
func (s *Store) File(ctx context.Context, in FilingInput) (Filing, error) {
	if strings.TrimSpace(in.EntityID) == "" {
		return Filing{}, fmt.Errorf("%w: a filing names the topic it files under; "+
			"a record about nothing in particular is recorded with NoTopic and a reason", ErrInvalidValue)
	}
	return s.appendFiling(ctx, filingWrite{
		record:    in.Record,
		entityID:  strings.TrimSpace(in.EntityID),
		rationale: in.Rationale,
		author:    in.Author,
		authorID:  in.AuthorID,
		heuristic: in.Heuristic,
	})
}

// NoTopic records §4.13's other honest result: this record is about nothing in
// particular, and here is why.
//
// It is a filing with no topic rather than the absence of one, because the two
// are different states and the triage recipe has to tell them apart: an
// unfiled record is one nobody has considered, and this is one somebody
// considered and answered.
func (s *Store) NoTopic(ctx context.Context, record Ref, author FilingAuthor,
	authorID, reason string) (Filing, error) {
	if strings.TrimSpace(reason) == "" {
		return Filing{}, fmt.Errorf("%w: recording that a record is about nothing in particular "+
			"is an answer and needs its reason", ErrInvalidValue)
	}
	return s.appendFiling(ctx, filingWrite{
		record:    record,
		rationale: reason,
		author:    author,
		authorID:  authorID,
	})
}

// Unfile withdraws a filing, with the reason kept verbatim.
//
// It appends rather than removing, so the record's history says that it was
// filed here, by whom, and why that was undone. A record that is not filed
// under the topic is ErrNotFiled: withdrawing a filing that does not exist
// would write a history nobody made.
func (s *Store) Unfile(ctx context.Context, record Ref, entityID string,
	author FilingAuthor, authorID, reason string) (Filing, error) {
	if strings.TrimSpace(reason) == "" {
		return Filing{}, fmt.Errorf("%w: unfiling a record needs its reason", ErrInvalidValue)
	}
	current, found, err := s.newestFiling(ctx, s.db, record, strings.TrimSpace(entityID))
	if err != nil {
		return Filing{}, err
	}
	if !found || current.Withdrawn {
		return Filing{}, fmt.Errorf("%w: %s %s under topic %q",
			ErrNotFiled, record.Type, record.ID, entityID)
	}
	return s.appendFiling(ctx, filingWrite{
		record:   record,
		entityID: strings.TrimSpace(entityID),
		// The withdrawn row carries the rationale it withdrew, so a reader
		// of one row sees what was claimed as well as what ended it. The
		// alternative — an empty rationale — would make the history readable
		// only by joining rows nobody asked it to join.
		rationale:      current.Rationale,
		author:         author,
		authorID:       authorID,
		heuristic:      current.Heuristic,
		withdrawn:      true,
		withdrawReason: reason,
	})
}

// filingWrite is one row about to be appended: the fields the callers above
// differ in, so the write path itself has no opinion about which of them it is
// serving.
type filingWrite struct {
	record         Ref
	entityID       string
	rationale      string
	author         FilingAuthor
	authorID       string
	heuristic      bool
	withdrawn      bool
	withdrawReason string
}

func (s *Store) appendFiling(ctx context.Context, in filingWrite) (Filing, error) {
	if !in.author.valid() {
		return Filing{}, fmt.Errorf("%w: filing author %q", ErrInvalidValue, in.author)
	}
	switch {
	case in.author == FilingHeuristic && in.authorID != "":
		return Filing{}, fmt.Errorf("%w: a heuristic filing is not somebody's act and carries no author id",
			ErrInvalidValue)
	case in.author != FilingHeuristic && strings.TrimSpace(in.authorID) == "":
		return Filing{}, fmt.Errorf("%w: a filing by a %s names who filed it", ErrInvalidValue, in.author)
	}
	if strings.TrimSpace(in.rationale) == "" {
		return Filing{}, fmt.Errorf("%w: a filing says why the record belongs to the topic", ErrInvalidValue)
	}
	// A heuristic is heuristic whatever the caller passed: §4.13 requires
	// seeded filings to be labelled so the recipe knows to revisit them, and
	// a flag the seeding path could forget would leave them indistinguishable
	// from a judgement.
	heuristic := in.heuristic || in.author == FilingHeuristic
	payload := FilingPayload{Rationale: in.rationale, WithdrawReason: in.withdrawReason}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Filing{}, err
	}
	id, err := newID("fil")
	if err != nil {
		return Filing{}, err
	}
	record := Filing{
		ID:             id,
		Record:         in.record,
		EntityID:       in.entityID,
		Rationale:      in.rationale,
		Author:         in.author,
		AuthorID:       in.authorID,
		Heuristic:      heuristic,
		Withdrawn:      in.withdrawn,
		WithdrawReason: in.withdrawReason,
		CreatedAt:      s.now(),
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		// The filed record must exist and may be any of the four kinds: an
		// observation is filed under a topic exactly like a proposal, because
		// §4.13 is about what a record is about and not about who rules on
		// it.
		if err := s.requireSubject(ctx, tx, in.record, false); err != nil {
			return fmt.Errorf("filed record: %w", err)
		}
		current, found, err := s.newestFiling(ctx, tx, in.record, in.entityID)
		if err != nil {
			return err
		}
		if found {
			record.SupersedesID = current.ID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_filing(
			id, record_kind, record_id, entity_id, author, author_id, heuristic, withdrawn,
			supersedes_id, schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, string(record.Record.Type), record.Record.ID, record.EntityID,
			string(record.Author), record.AuthorID, record.Heuristic, record.Withdrawn,
			nullableID(record.SupersedesID), RecordSchema, formatTime(record.CreatedAt),
			encoded); err != nil {
			return fmt.Errorf("insert filing: %w", err)
		}
		staged, err := stagedFiling(record, encoded)
		if err != nil {
			return err
		}
		// A run's filing joins that run's closure, and an operator's or a
		// heuristic's is a closure of one, on the terms internal/sync's
		// Append settles for every other write: nobody resumes an operator's
		// act, so a record staged into somebody else's closed closure would
		// stay pending forever.
		pub, err = s.stage(ctx, tx, filingRun(record), staged)
		return err
	})
	if err != nil {
		return Filing{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Filing{}, err
	}
	s.mintAbout(ctx, record)
	return record, nil
}

// filingRun reports the run whose closure a filing belongs to, and the empty
// string for one no run made.
func filingRun(record Filing) string {
	if record.Author == FilingRun {
		return record.AuthorID
	}
	return ""
}

// FilingsOf reads one record's whole filing history, newest first: live
// filings, withdrawn ones, and the no-topic answers, because all three are
// things somebody recorded about this record.
func (s *Store) FilingsOf(ctx context.Context, record Ref) ([]Filing, error) {
	return s.filings(ctx, `f.record_kind = ? AND f.record_id = ?
		ORDER BY f.created_at DESC, f.id DESC`, string(record.Type), record.ID)
}

// FiledUnder lists the records currently filed under one topic, newest filing
// first. Withdrawn and superseded filings are absent by construction: the
// question is what is in this topic now, and its history is read from the
// records' own filing lists.
func (s *Store) FiledUnder(ctx context.Context, entityID string) ([]Ref, error) {
	rows, err := s.filings(ctx, `f.entity_id = ? AND `+filingLive+`
		ORDER BY f.created_at DESC, f.id DESC`, entityID)
	if err != nil {
		return nil, err
	}
	refs := make([]Ref, 0, len(rows))
	for _, filing := range rows {
		refs = append(refs, filing.Record)
	}
	return refs, nil
}

// EntitiesFiled reports the topics one record is currently filed under.
//
// Heuristic filings are included and retired topics are not, which is the
// asymmetry the two facts justify: a seeded filing is still where the record
// sits until something revisits it, and a retired topic is one the operator
// said should never have existed, so reading a record's topics through it
// would answer with a subject that is no longer one.
func (s *Store) EntitiesFiled(ctx context.Context, record Ref) ([]string, error) {
	rows, err := s.filings(ctx, `f.record_kind = ? AND f.record_id = ? AND f.entity_id <> ''
		AND `+filingLive+` ORDER BY f.created_at DESC, f.id DESC`,
		string(record.Type), record.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, filing := range rows {
		retired, err := s.retired(ctx, filing.EntityID)
		if err != nil {
			return nil, err
		}
		if !retired {
			ids = append(ids, filing.EntityID)
		}
	}
	return ids, nil
}

// Unfiled is §4.13's triage backlog: the open records nothing has judged.
//
// Three exclusions, each with its own reason. A record whose ruling was
// `reject` or `duplicate` is closed, and asking a recipe to say what a
// rejected record is about spends a model on a question nobody will read the
// answer to. A superseded wording is represented by the revision that replaced
// it, so filing both would file one claim twice. And a record with a live
// filing by an operator or a run has been judged — while one filed only by a
// heuristic has not, which is exactly what §4.13 means by "unfiled or filed
// only by a heuristic".
//
// A filing under a retired topic counts as absent, which is how retirement
// re-queues a topic's filings without touching a row.
//
// Oldest first, because the backlog is a queue and a queue nobody drains from
// the bottom has a permanent bottom.
func (s *Store) Unfiled(ctx context.Context, limit int) ([]Ref, error) {
	bound, _ := ListFilter{Limit: limit}.bounds()
	judged, err := s.judgedRecords(ctx)
	if err != nil {
		return nil, err
	}
	standings, err := s.ReviewStandings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, unfiledCandidates)
	if err != nil {
		return nil, fmt.Errorf("read open records: %w", err)
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() && len(out) < bound {
		var (
			kind      string
			id        string
			createdAt string
		)
		if err := rows.Scan(&kind, &id, &createdAt); err != nil {
			return nil, fmt.Errorf("read open records: %w", err)
		}
		ref := Ref{Type: EntityType(kind), ID: id}
		if judged[ref] {
			continue
		}
		switch standings[ref].Status {
		case ReviewRejected, ReviewDuplicate:
			continue
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// judgedRecords is the set of records a person or a run has already said
// something about: filed under a live topic, or answered with no topic at all.
//
// It is one query rather than a lookup per candidate because the backlog is a
// scan and the filing table is small — a topic decision per record, against a
// corpus of records — so the whole judgement set is cheaper to hold than one
// round trip per row would be.
func (s *Store) judgedRecords(ctx context.Context) (map[Ref]bool, error) {
	// heuristic = 0 and the author check are the same rule stated twice, and
	// both are needed: the author says who filed, and the flag says that
	// whoever filed was guessing (§4.13 lets a run label its own filing as
	// one). Either mark makes the filing a thing the recipe still owes a
	// judgement.
	rows, err := s.filings(ctx, `f.author <> ? AND f.heuristic = 0 AND `+filingLive,
		string(FilingHeuristic))
	if err != nil {
		return nil, err
	}
	judged := make(map[Ref]bool, len(rows))
	for _, filing := range rows {
		if filing.EntityID != "" {
			retired, err := s.retired(ctx, filing.EntityID)
			if err != nil {
				return nil, err
			}
			if retired {
				continue
			}
		}
		judged[filing.Record] = true
	}
	return judged, nil
}

// retired asks the ledger whether a topic has been retired, memoizing nothing:
// the answer is read per call because a retirement during a long scan should
// take effect, and the caller's set of topics is small enough that it costs
// one indexed read each.
//
// A ledger that cannot answer is an error rather than a false, because
// treating "I could not ask" as "not retired" would re-file records under a
// topic the operator retired and call it a fact.
func (s *Store) retired(ctx context.Context, entityID string) (bool, error) {
	if s.entities == nil || entityID == "" {
		return false, nil
	}
	retired, err := s.entities.EntityRetired(ctx, entityID)
	if err != nil {
		return false, fmt.Errorf("frontier: ask whether topic %s is retired: %w", entityID, err)
	}
	return retired, nil
}

// unfiledCandidates enumerates the head revision of every record, oldest
// first.
//
// The head filter is a NOT EXISTS over each table's own ancestor column, which
// is indexed for hypotheses and a scan for the other three. That is affordable
// here and deliberately not made cheaper with a stored flag: a `is_head`
// column would be a second answer to a question the rows already answer, and
// this package has no UPDATE with which to maintain it.
const unfiledCandidates = `
SELECT kind, id, created_at FROM (
	SELECT 'hypothesis' AS kind, h.id AS id, h.created_at AS created_at
		FROM frontier_hypothesis h
		WHERE NOT EXISTS (SELECT 1 FROM frontier_hypothesis d WHERE d.ancestor_id = h.id)
	UNION ALL
	SELECT 'observation', o.id, o.created_at FROM frontier_observation o
		WHERE NOT EXISTS (SELECT 1 FROM frontier_observation d WHERE d.ancestor_id = o.id)
	UNION ALL
	SELECT 'finding', n.id, n.created_at FROM frontier_finding n
		WHERE NOT EXISTS (SELECT 1 FROM frontier_finding d WHERE d.ancestor_id = n.id)
	UNION ALL
	SELECT 'proposal', p.id, p.created_at FROM frontier_proposal p
		WHERE NOT EXISTS (SELECT 1 FROM frontier_proposal d WHERE d.ancestor_id = p.id)
) ORDER BY created_at, id`

// filingLive is what makes one row the filing that currently holds: it is not
// a withdrawal, and no later row about the same record and topic replaced it.
//
// It is derived rather than stored for the reason the review status is: a
// stored flag would need an UPDATE, this package has none, and a flag that
// could disagree with the rows would make the history unreadable exactly when
// somebody needed it. The comparison is spelled out rather than written as a
// row value because a filing's order is (created_at, id) and the fixed-width
// timestamp makes text order time order.
const filingLive = `f.withdrawn = 0 AND NOT EXISTS (
	SELECT 1 FROM frontier_filing later
	WHERE later.record_kind = f.record_kind
		AND later.record_id = f.record_id
		AND later.entity_id = f.entity_id
		AND (later.created_at > f.created_at
			OR (later.created_at = f.created_at AND later.id > f.id)))`

const filingSelect = `SELECT f.id, f.record_kind, f.record_id, f.entity_id, f.author,
	f.author_id, f.heuristic, f.withdrawn, COALESCE(f.supersedes_id, ''), f.created_at,
	f.payload_json FROM frontier_filing f WHERE `

func (s *Store) filings(ctx context.Context, where string, args ...any) ([]Filing, error) {
	rows, err := s.db.QueryContext(ctx, filingSelect+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read filings: %w", err)
	}
	defer rows.Close()
	var out []Filing
	for rows.Next() {
		record, err := scanFiling(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// newestFiling reads the row that currently answers for one record and one
// topic, whether it is a filing or the withdrawal of one. It runs inside the
// caller's transaction when there is one, so the row a write supersedes cannot
// change between the read and the insert.
func (s *Store) newestFiling(ctx context.Context, q querier, record Ref, entityID string) (
	Filing, bool, error) {
	rows, err := q.QueryContext(ctx, filingSelect+`f.record_kind = ? AND f.record_id = ?
		AND f.entity_id = ? ORDER BY f.created_at DESC, f.id DESC LIMIT 1`,
		string(record.Type), record.ID, entityID)
	if err != nil {
		return Filing{}, false, fmt.Errorf("read filings: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return Filing{}, false, rows.Err()
	}
	current, err := scanFiling(rows)
	if err != nil {
		return Filing{}, false, err
	}
	return current, true, nil
}

func scanFiling(row interface{ Scan(...any) error }) (Filing, error) {
	var (
		record    Filing
		kind      string
		author    string
		createdAt string
		payload   []byte
	)
	if err := row.Scan(&record.ID, &kind, &record.Record.ID, &record.EntityID, &author,
		&record.AuthorID, &record.Heuristic, &record.Withdrawn, &record.SupersedesID,
		&createdAt, &payload); err != nil {
		return Filing{}, fmt.Errorf("read filing: %w", err)
	}
	record.Record.Type = EntityType(kind)
	record.Author = FilingAuthor(author)
	var err error
	if record.CreatedAt, err = parseTime(createdAt); err != nil {
		return Filing{}, fmt.Errorf("filing %s: %w", record.ID, err)
	}
	var decoded FilingPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Filing{}, fmt.Errorf("decode filing %s payload: %w", record.ID, err)
	}
	record.Rationale = decoded.Rationale
	record.WithdrawReason = decoded.WithdrawReason
	return record, nil
}

// mintAbout records the graph shadow of one filing (#113, §4.13).
//
// It is a shadow in the exact sense mintSupersedes is: the filing row stays
// the authority — it carries the rationale, the author and the history — and
// the edge is what makes the same fact reachable from the corpus-wide graph a
// reader browses without knowing which table established anything.
//
// Only a live filing under a topic mints one. A withdrawal cannot: an edge is
// append-only and idempotent on (kind, from, to), so re-asserting the same
// triple would return the edge already recorded and asserting its negation is
// not something the graph has a shape for. The withdrawal publishes as a
// record of its own, which is where a reader learns that the citation stopped
// holding. A no-topic answer mints nothing because it names no target, and an
// edge to the empty string would be a citation of nothing.
func (s *Store) mintAbout(ctx context.Context, record Filing) {
	if s.refs == nil || record.Withdrawn || record.EntityID == "" {
		return
	}
	s.appendEdge(ctx, reference.Edge{
		Kind:      reference.KindAbout,
		From:      recordRef(record.Record.Type, record.Record.ID),
		To:        reference.RecordRef{Kind: entityNamespace, ID: record.EntityID},
		ActorKind: filingActorKind(record.Author),
		ActorRef:  record.AuthorID,
		// No note. The rationale is on the filing, which is where §4.13 puts
		// it and where an operator reads it; copying the sentence onto the
		// edge would make one claim exist twice and let the two disagree.
	})
}

// filingActorKind maps a filing's author onto the reference graph's three
// actors. A heuristic is `system` and never `run`: nothing invoked a model,
// and attribution that dresses machinery as a reviewer is attribution nobody
// can audit.
func filingActorKind(author FilingAuthor) string {
	switch author {
	case FilingOperator:
		return string(ActorOperator)
	case FilingRun:
		return string(ActorRun)
	}
	return "system"
}

// entityNamespace is the record namespace a topic lives in.
//
// It is the string internal/reference/resolve registers the Reality Ledger's
// entity resolver under, spelled here rather than imported: that package
// imports this one to build the registry, so the constant cannot travel the
// other way without a cycle. The pair is not left to agree by hope — this
// package's test asserts the literal against the namespace the resolver
// registry actually holds.
const entityNamespace = "reality_entity"
