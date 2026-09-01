package sharedcatalog

import (
	"context"
	"strings"
	"testing"
)

// These tests defend migrations/0010's bargain (issue #114): a proposal's
// provenance travels in the clear so a fleet host with no payload key can tell a
// candidate proposal - an unbacked want addressing a hypothesis - from a
// consolidated one that rests on findings, and the proposal's words do not
// travel at all.
//
// Both halves need proving, and the second is the one that could rot silently. A
// leak scan that finds nothing proves nothing unless the table it is scanning
// has rows in it, so the case below publishes a real proposal whose claim
// carries the suite's sentinel.

// proposalClosure builds a one-record run whose record is a proposal: the claim
// in the sealed payload, the provenance in the plaintext half.
func proposalClosure(runID, recordID string, subjects []RecordSubject, claim string) RunClosure {
	return RunClosure{
		RunID:            runID,
		DeploymentID:     "d1",
		OriginInstanceID: "inst-a",
		RecordCount:      1,
		Records: []StagedRecord{{
			RecordID: recordID,
			Kind:     KindProposal,
			Schema:   1,
			Ordinal:  0,
			Payload:  []byte(`{"schema":1,"claim":"` + claim + `"}`),
			Subjects: subjects,
		}},
	}
}

// candidateSubjects is #114's new form: hypotheses addressed directly, resting
// on no finding at all. Two of them, because the relation is many-to-many and
// their order is the producer's.
func candidateSubjects() []RecordSubject {
	return []RecordSubject{
		{Kind: SubjectHypothesis, ID: "hyp_0f1e2d3c4b5a69788796a5b4c3d2e1f0"},
		{Kind: SubjectHypothesis, ID: "hyp_1122334455667788990011223344556"},
	}
}

// The point of the whole table: a host holding only the catalog credential can
// read what a proposal rests on, across every machine, and cannot read a word of
// what it proposes.
func TestProposalPublishesItsSubjectsAndSealsItsClaim(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	want := candidateSubjects()
	mustSync(t, db, store, ring, proposalClosure("run-prop", "prop-1", want, sentinel))

	got, err := RecordSubjects(ctx, db, []string{"prop-1"})
	if err != nil {
		t.Fatalf("RecordSubjects: %v", err)
	}
	read := got["prop-1"]
	if len(read) != len(want) {
		t.Fatalf("read %d subjects, want %d", len(read), len(want))
	}
	// Insertion order is the answer, not a set: "the first hypothesis this
	// proposal addresses" is a question with one answer, and position is what
	// keeps it.
	for i := range want {
		if read[i] != want[i] {
			t.Errorf("subject %d is %s, want %s", i, read[i], want[i])
		}
	}

	// The claim is not in PostgreSQL. The scan is schema-reflecting, so it
	// covers analysis_proposal_subjects without being told the table exists.
	for _, hit := range scanSchemaForText(t, db, sentinel) {
		t.Errorf("a proposal's claim is in PostgreSQL in %s", hit)
	}

	// Non-vacuity for the new table specifically. The same scan must FIND a
	// subject id, or its silence about the claim says nothing about
	// analysis_proposal_subjects - it would be the silence of a table with no
	// rows.
	hits := scanSchemaForText(t, db, want[0].ID)
	var found bool
	for _, hit := range hits {
		if strings.HasPrefix(hit, "analysis_proposal_subjects.subject_id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the plaintext scan did not find the proposal's own subject in "+
			"analysis_proposal_subjects.subject_id (hits: %v); it is not reading this table, "+
			"so its verdict about the claim is vacuous", hits)
	}

	// And the claim is readable where it belongs: in the sealed object, with a
	// key, on this machine.
	records, err := AnalysisRecords(ctx, db, "run-prop")
	if err != nil {
		t.Fatalf("AnalysisRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("run holds %d records, want 1", len(records))
	}
	object, err := store.Get(ctx, records[0].ObjectKey)
	if err != nil {
		t.Fatalf("read the sealed object: %v", err)
	}
	if contains(object, sentinel) {
		t.Error("the stored object holds the claim in the clear")
	}
	plaintext, err := OpenRecord(ctx, store, ring, records[0])
	if err != nil {
		t.Fatalf("open the proposal record: %v", err)
	}
	if !contains(plaintext, sentinel) {
		t.Error("the decrypted proposal carries no claim; the assertions above are vacuous")
	}
}

// The two forms #114 makes lawful must be distinguishable from the plaintext
// columns alone, because that is the entire reason the columns exist: a host
// with no payload key that cannot tell them apart renders an unbacked want with
// the authority of a verified conclusion.
func TestConsolidatedAndCandidateFormsAreDistinguishableWithoutAKey(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	consolidated := []RecordSubject{
		{Kind: SubjectFinding, ID: "fnd_aabbccddeeff00112233445566778899"},
		{Kind: SubjectHypothesis, ID: "hyp_0f1e2d3c4b5a69788796a5b4c3d2e1f0"},
	}
	mustSync(t, db, store, ring, proposalClosure("run-cons", "prop-cons", consolidated, sentinel))
	mustSync(t, db, store, ring, proposalClosure("run-cand", "prop-cand", candidateSubjects(), sentinel))
	// A record from before #114: no subjects staged, because nothing back-fills
	// one. Its form is unknown, and unknown must not read as candidate.
	mustSync(t, db, store, ring, proposalClosure("run-old", "prop-old", nil, sentinel))

	got, err := RecordSubjects(ctx, db, []string{"prop-cons", "prop-cand", "prop-old"})
	if err != nil {
		t.Fatalf("RecordSubjects: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RecordSubjects answered for %d records, want 2 - a proposal with no "+
			"subjects is omitted rather than answered with an empty list", len(got))
	}
	if _, present := got["prop-old"]; present {
		t.Error("a proposal written before #114 was answered for; absence is the honest answer")
	}

	// The form is derived from which subject kinds are present, never stored.
	if !hasFinding(got["prop-cons"]) {
		t.Errorf("the consolidated proposal carries no finding subject: %v", got["prop-cons"])
	}
	if hasFinding(got["prop-cand"]) {
		t.Errorf("the candidate proposal carries a finding subject: %v", got["prop-cand"])
	}
}

func hasFinding(subjects []RecordSubject) bool {
	for _, s := range subjects {
		if s.Kind == SubjectFinding {
			return true
		}
	}
	return false
}

// The projection the fleet subject read selects must be plaintext-eligible by
// the same gate the record and edge reads pass, and pointed at the query rather
// than at a comment about the query.
func TestSubjectProjectionIsPlaintextEligible(t *testing.T) {
	for table, columns := range SubjectProjection() {
		if err := AssertPhaseBPlaintext(table, columns...); err != nil {
			t.Errorf("the fleet subject projection is not plaintext-eligible: %v", err)
		}
	}
	// Every column of the new table is covered, so a migration adding one
	// cannot slip past the gate by not being selected yet.
	for column := range allowlist["analysis_proposal_subjects"] {
		if err := AssertPhaseBPlaintext("analysis_proposal_subjects", column); err != nil {
			t.Errorf("analysis_proposal_subjects.%s: %v", column, err)
		}
	}
	// The narrower Phase B question still bites on this table: a column of a
	// class the general allowlist admits and Phase B refuses fails here.
	if err := AssertPhaseBPlaintext("analysis_proposal_subjects", "rationale"); err == nil {
		t.Error("the Phase B gate accepted a column no allowlist entry describes")
	}
}

// An empty id list queries nothing and answers with an empty map. A caller with
// nothing to ask about must not accidentally read the whole deployment, and the
// nil map a lazier implementation would return would panic the first caller that
// indexed it.
func TestRecordSubjectsWithNoIDsQueriesNothing(t *testing.T) {
	got, err := RecordSubjects(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("RecordSubjects with no ids: %v", err)
	}
	if got == nil {
		t.Fatal("RecordSubjects answered with a nil map rather than an empty one")
	}
	if len(got) != 0 {
		t.Errorf("RecordSubjects answered for %d records, want none", len(got))
	}
	// The nil *sql.DB is the assertion: reaching the database at all would have
	// panicked, so passing proves no query was issued.
}

// A staged subject the catalog could not carry must be refused before anything
// is written, because analysis_records is insert-only and a malformed record
// cannot be corrected there.
func TestSyncRunRefusesMalformedSubjects(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(*RunClosure)
	}{
		{
			name: "an unknown subject kind",
			mut:  func(c *RunClosure) { c.Records[0].Subjects[0].Kind = "observation" },
		},
		{
			name: "a subject with no id",
			mut:  func(c *RunClosure) { c.Records[0].Subjects[0].ID = "" },
		},
		{
			name: "an id that is prose rather than an identifier",
			mut:  func(c *RunClosure) { c.Records[0].Subjects[0].ID = "the hypothesis about caching" },
		},
		{
			// The pairing matters: a subject list on a record that is not a
			// proposal would make analysis_proposal_subjects mean two things,
			// and it has no column that says which row is which.
			name: "subjects on a record that is not a proposal",
			mut:  func(c *RunClosure) { c.Records[0].Kind = KindHypothesis },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closure := proposalClosure("run-bad", "prop-bad", candidateSubjects(), sentinel)
			tc.mut(&closure)
			if _, err := SyncRun(ctx, db, store, ring, closure); err == nil {
				t.Fatal("SyncRun accepted a proposal subject the catalog cannot carry")
			}
			if store.putCount() != 0 {
				t.Errorf("a refused proposal wrote %d objects; refusal must precede sealing",
					store.putCount())
			}
			var rows int
			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM analysis_proposal_subjects`).Scan(&rows); err != nil {
				t.Fatalf("count subjects: %v", err)
			}
			if rows != 0 {
				t.Errorf("a refused proposal left %d rows", rows)
			}
		})
	}
}

// The CHECK is the second line of defence and has to hold on its own. The
// writer's validation is a courtesy to the caller; a vocabulary that only the
// writer enforced would admit whatever a future writer, a migration or a manual
// statement happened to insert.
func TestUnknownSubjectKindIsRefusedByTheDatabase(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)

	mustSync(t, db, store, ring,
		proposalClosure("run-check", "prop-check", candidateSubjects(), sentinel))

	if _, err := db.Exec(`
		INSERT INTO analysis_proposal_subjects (record_id, position, subject_kind, subject_id)
		VALUES ('prop-check', 9, 'observation', 'obs-1')`); err == nil {
		t.Error("the database accepted a subject kind migrations/0010 does not define")
	}
}

// A proposal's provenance is append-only in the database, not merely in the
// writer. A proposal that rested on different evidence is a different proposal,
// answered by a later record (SPEC.md §4.7), and a statement that could rewrite
// this one would make provenance a claim rather than a record.
func TestPublishedProposalSubjectsAreAppendOnly(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring,
		proposalClosure("run-frozen", "prop-frozen", candidateSubjects(), sentinel))

	if _, err := db.ExecContext(ctx,
		`UPDATE analysis_proposal_subjects SET subject_kind = 'finding'
		  WHERE record_id = 'prop-frozen'`); err == nil {
		t.Error("a candidate proposal was rewritten into a finding-backed one")
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM analysis_proposal_subjects WHERE record_id = 'prop-frozen'`); err == nil {
		t.Error("a proposal's provenance was deleted")
	}

	// A second sync of the same run is a no-op rather than a second set of
	// rows: the record is already present, so nothing is re-sealed and nothing
	// is re-inserted.
	res := mustSync(t, db, store, ring,
		proposalClosure("run-frozen", "prop-frozen", candidateSubjects(), sentinel))
	if res.ObjectsWritten != 0 || res.RecordsCommitted != 0 {
		t.Errorf("a replayed proposal wrote %d objects and %d rows, want none",
			res.ObjectsWritten, res.RecordsCommitted)
	}
	got, err := RecordSubjects(ctx, db, []string{"prop-frozen"})
	if err != nil {
		t.Fatalf("RecordSubjects: %v", err)
	}
	if len(got["prop-frozen"]) != 2 {
		t.Fatalf("the replay left %d subjects, want 2", len(got["prop-frozen"]))
	}
}
