package explore

import (
	"fmt"
	"path/filepath"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/reference"
)

// This file is the emission half of issue #113 for the two link forms only a
// run knows about: which session a claim's evidence came out of, and which
// prior outputs a run had in front of it when it produced a record.
//
// internal/frontier mints the other two - supersedes and duplicates - because
// both endpoints are its own rows and it already resolves the actor. The two
// here need the run's world instead. A locator names a file, and only this
// package knows which of the run's fixed sessions that file is - the
// preparation's scope paired with the preflight inputs that carry the paths -
// after which an injected deriver turns that session into the deployment-wide
// key the graph addresses it by. A record's inspiration is the refine-first
// context that was injected into the job that produced it, which nothing
// downstream of the job can reconstruct.
//
// The same discipline applies as in internal/frontier. The byte-precise
// locator on the observation stays the authority for what the claim rests on;
// the edge only says which session those bytes live in, so a graph reader can
// walk from a session to every claim drawn from it without opening a payload.
// Nothing here is a precondition for a durable write: a nil Appender mints no
// edges, a failure is a recorded warning, and the run's verdict never turns on
// whether the graph was writable.

// FailureReference reports an edge the reference graph refused (#113).
//
// It is recorded through state.warn rather than state.fail, which is the whole
// distinction: an edge is a shadow of a record that is already durable, so a
// missing edge degrades navigation and nothing else. A run reported as
// degraded because its graph was unwritable would be a run whose analysis
// nobody trusts over a link somebody can re-mint.
const FailureReference = "reference"

// The hedges the edges this package mints carry.
//
// Both are fixed strings written here, never text from a model or from a
// record's payload. That is deliberate: SPEC.md §763 admits the edge kind and
// its endpoints in the clear while the note travels sealed with the rest of
// the content, so a note that restated a payload would be a second copy of it
// free to drift - and a note assembled from worker output would put untrusted
// prose into the one field a render surface shows beside a link.
const (
	// edgeNoteEvidence states which half of the pair is the authority. §4.3
	// makes evidence inseparable from its locator, and the locator is on the
	// observation: this edge narrows to a session, not to bytes.
	edgeNoteEvidence = "the citing record's own locator recovers the cited bytes; " +
		"this edge names only the session they were drawn from"
	// edgeNoteInspiredBy refuses to overclaim. Babel knows the run received
	// the cited record as refine-first context (#87) and does not know that
	// the record it then produced came from it - a run may read twelve prior
	// candidates and be moved by none of them - so the edge records the
	// adjacency it can defend and says that is what it is.
	edgeNoteInspiredBy = "the run that produced this record received the cited record as " +
		"refine-first context; recorded adjacency, not a claim of derivation"
)

// actorRun is the edge attribution every edge this package mints carries: a
// run asserted it, identified by the run id its receipt carries (#96).
//
// It is derived from frontier.ActorRun rather than written as a literal. The
// two vocabularies are the same three words - reference.Edge's own doc names
// "operator", "run" and "system" - and deriving from the one this package
// already writes into revision chains is what keeps a run's attribution
// identical in the chain and in the graph.
const actorRun = string(frontier.ActorRun)

// sessionSource is the identity of one session in this run's scope, as the
// locator that points into it does not carry: a locator names a file, and the
// graph addresses a session by its durable key.
type sessionSource struct {
	harness  string
	sourceID string
}

// sessionSources maps a session's primary log path to that session's identity.
//
// It is built from the preflight inputs rather than from the preparation
// because only the inputs carry the path, and New has already refused a
// mismatch between the two: validateSelection requires the inputs to cover
// exactly the preparation, so a path resolved here names a session this run's
// scope actually fixed.
func sessionSources(inputs []preflight.Input) map[string]sessionSource {
	sources := make(map[string]sessionSource, len(inputs))
	for _, in := range inputs {
		if in.Stream.Path == "" {
			continue
		}
		sources[filepath.Clean(in.Stream.Path)] = sessionSource{
			harness:  in.Stream.Harness,
			sourceID: in.Stream.SourceID,
		}
	}
	return sources
}

// stageRunID is the run identity one stage records under.
//
// §5.4's separate passes each get their own run identity, their own receipt
// and - because an edge's actor is the identity its receipt carries (#96) -
// their own attribution in the graph. Discovery runs under the exploration's
// own id; a challenger's objection is attributed to the challenger.
func stageRunID(runID string, stage Stage) string {
	switch stage {
	case StageChallenge, StageSynthesize:
		return runID + "/" + string(stage)
	}
	return runID
}

// mintEvidence records the graph shadow of one claim's evidence.
//
// One edge per distinct session the claim's locators name, not one per
// locator: three quotations from one session are three locators and one
// citation relationship, and the graph would say nothing extra by holding the
// same edge three times.
//
// A locator whose path is not one of this run's sessions is skipped in
// silence, and that is correct rather than a swallowed error. frontier.Evidence
// admits whole-object evidence - a repository blob, a brokered research
// document - which has a valid locator and no session behind it at all. The
// locator still recovers those bytes; there is simply no session endpoint to
// bind an edge to, and warning about every one would make the diagnostics path
// useless for the failures that matter.
func (c *Controller) mintEvidence(st *state, stage Stage, runID string,
	kind frontier.EntityType, id string, evidence []frontier.Evidence) {
	if c.cfg.References == nil || c.cfg.SessionKey == nil || id == "" {
		return
	}
	var seen map[string]bool
	for _, ev := range evidence {
		source, ok := c.sessions[filepath.Clean(ev.Locator().Path)]
		if !ok {
			continue
		}
		key := c.cfg.SessionKey(source.harness, source.sourceID)
		if key == "" || seen[key] {
			continue
		}
		if seen == nil {
			seen = make(map[string]bool, len(evidence))
		}
		seen[key] = true
		c.appendEdge(st, stage, reference.Edge{
			Kind:      reference.KindEvidence,
			From:      reference.RecordRef{Kind: string(kind), ID: id},
			To:        reference.RecordRef{Kind: SessionRecordKind, ID: key},
			ActorKind: actorRun,
			ActorRef:  runID,
			Note:      edgeNoteEvidence,
		})
	}
}

// SessionRecordKind is the reference-graph namespace for a session. It is
// exported because the render surfaces have to recognize it to turn a session
// endpoint into a link, and a string literal repeated on both sides of that
// boundary is a typo away from an edge nobody can follow.
const SessionRecordKind = "session"

// graphNamespace reports the reference-graph namespace of one searchable
// frontier output, and whether the graph can address it at all.
//
// The first three frontier.OutputKind values are entity kinds spelled the same
// way and resolve as records. OutputReviewAnswer is not one of them: it is a
// disposition or the refinement request a rejection authorized, which is an
// operator's answer *about* a record rather than a record that develops, so no
// resolver namespace names it and write-time validation would refuse an edge
// bound to it. It would also be the wrong relation - "the run was shown a
// decision" is addresses-shaped, and #113's vocabulary is closed - so the
// injection is recorded in the job document, which is where a worker reads it,
// and not in the graph.
func graphNamespace(kind frontier.OutputKind) (string, bool) {
	switch kind {
	case frontier.OutputHypothesis, frontier.OutputObservation, frontier.OutputFinding:
		return string(kind), true
	}
	return "", false
}

// mintInspiredBy records the graph shadow of the refine-first injection.
//
// It fires for every record the attempt reached, including one recognized from
// a prior attempt rather than written now. That is on purpose: an attempt that
// wrote a record and was killed before its edges landed gets them on resume,
// and re-appending an edge is idempotent on (kind, from, to) by the Appender's
// contract, so the honest recovery costs nothing.
//
// The cross product is bounded by run.MaxRelatedOutputs, which is twelve, and
// it is a cross product on purpose. The injection was made at the job level -
// every stage carries the refine-first context - so what Babel can say is that
// this record came out of a run that had been shown those, which is exactly
// what edgeNoteInspiredBy says.
func (c *Controller) mintInspiredBy(st *state, stage Stage, kind frontier.EntityType, id string) {
	if c.cfg.References == nil || id == "" || len(st.injected) == 0 {
		return
	}
	runID := stageRunID(st.opt.RunID, stage)
	from := reference.RecordRef{Kind: string(kind), ID: id}
	for _, to := range st.injected {
		if to == from {
			// A record revising one that was injected into the same run is
			// its own inspiration by this rule. The chain already says what
			// happened there, and reference.Edge.Validate refuses a
			// self-reference, so this would be a guaranteed warning.
			continue
		}
		c.appendEdge(st, stage, reference.Edge{
			Kind:      reference.KindInspiredBy,
			From:      from,
			To:        to,
			ActorKind: actorRun,
			ActorRef:  runID,
			Note:      edgeNoteInspiredBy,
		})
	}
}

// appendEdge appends one edge and records a refusal as a warning.
//
// The commit context is used rather than the run's, for the reason every
// durable write in this package uses it: cancellation stops exploration and
// never stops recording what exploration already produced.
func (c *Controller) appendEdge(st *state, stage Stage, e reference.Edge) {
	if _, err := c.cfg.References.Append(st.commit, e); err != nil {
		st.warn(stage, FailureReference, c.now(), fmt.Errorf(
			"explore: record the %s edge from %s to %s: %w", e.Kind, e.From, e.To, err))
	}
}
