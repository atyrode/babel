package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	runstore "github.com/atyrode/babel/internal/run"
)

const prepareUsage = `Usage: babel prepare [SELECTOR...] [flags]

Fixes an exploration's corpus scope and emits an immutable preparation ID
(SPEC.md §6.5, §8). With no selector the scope is every session the source
adapters can see on this host; with selectors it is exactly those sessions.

The identity is derived from the content of the selection — host, harness,
source identity, and both digests per session — so naming it on a later
"babel explore --preparation ID" states which corpus that run read instead
of leaving it to whatever the machine happened to hold at the time.

Each selected session is also read into the local retrieval index, which is
the corpus a run's search tool is served from. The index is a rebuildable
cache under the cache directory; the preparation record is durable state.

Babel's own prior output is indexed alongside it and searched with the
scope's own salient terms, mechanically and with no model involved. The
records that come back are recorded in the preparation, and the run that
explores it receives them as prior candidate ideas to refine, revive, or
amend rather than duplicate (issue #87).

Nothing is sent anywhere and no repository is opened: this command reads
local files and writes local state only.

Flags:
  --harness NAME       restrict to one harness: omp, codex, or claude
  --roots DIR[,DIR]    scan these roots instead of the adapter defaults
  --host ID            archive host identity recorded in the selection
                       (default $BABEL_HOST_ID, else storage.json, else this
                       machine's hostname)
  --serendipitous      mark the scope as drawn for exploration, so the
                       related prior outputs reach the run as inspiration
                       rather than as constraint
  --json               emit the preparation as JSON on stdout

A selector is "HARNESS/SOURCE-ID", or any unambiguous suffix of one.
`

// relatedRow is one prior Babel output a preparation names, in the
// machine-readable document. The summary is not stored in the preparation —
// only the kind and the id are — so what is shown here is read back from the
// frontier and is the record's current wording.
type relatedRow struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Summary string  `json:"summary"`
	Status  string  `json:"status,omitempty"`
	Overlap float64 `json:"-"`
}

// preparedRow is one session in a preparation's machine-readable document.
// Digests are never truncated: a caller has to be able to compare them.
type preparedRow struct {
	Harness       string `json:"harness"`
	SourceID      string `json:"source_id"`
	Selector      string `json:"selector"`
	Host          string `json:"host"`
	CaptureDigest string `json:"capture_digest"`
	SourceDigest  string `json:"source_digest"`
	Bytes         int64  `json:"bytes"`
	Records       int    `json:"records"`
	Events        int    `json:"events"`
}

// prepareResult is `babel prepare --json`.
type prepareResult struct {
	PreparationID string        `json:"preparation_id"`
	PreparedAt    string        `json:"prepared_at"`
	Host          string        `json:"host"`
	Sessions      []preparedRow `json:"sessions"`
	IndexedEvents int           `json:"indexed_events"`
	// FrontierRecords is how many of Babel's own outputs the index now
	// holds, and SalientTerms is the query the scope produced. Both are
	// reported because the injection is mechanical: an operator who sees
	// unexpected related records needs to be able to see the terms that
	// found them.
	FrontierRecords int          `json:"frontier_records"`
	SalientTerms    []string     `json:"salient_terms,omitempty"`
	Related         []relatedRow `json:"related,omitempty"`
	Serendipitous   bool         `json:"serendipitous,omitempty"`
	Database        string       `json:"database"`
	Index           string       `json:"index"`
}

// prepare implements `babel prepare`.
func (a *app) prepare(ctx context.Context, args []string) error {
	c := newCmd("prepare", prepareUsage)
	var sf scanFlags
	var rf repoFlags
	sf.bindHarness(c)
	sf.bindRoots(c)
	c.fs.StringVar(&rf.host, "host", "", "archive host identity recorded in the selection")
	serendipitous := c.fs.Bool("serendipitous", false,
		"mark the scope as drawn for exploration rather than to answer something")
	asJSON := c.fs.Bool("json", false, "emit the preparation as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	ads, err := sf.selected(c)
	if err != nil {
		return err
	}
	host, err := rf.hostID(c)
	if err != nil {
		return err
	}

	sessions, _ := a.scan(ctx, ads, sf.rootList())
	chosen, err := selectSessions(c, sessions, c.args())
	if err != nil {
		return err
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	runs, err := runstore.Open(d.durableDir())
	if err != nil {
		return err
	}
	defer runs.Close()

	scoped, err := a.fixScope(ctx, runs, chosen, host, *serendipitous)
	if err != nil {
		return err
	}

	res := prepareResult{
		PreparationID:   string(scoped.prep.ID),
		PreparedAt:      formatTime(scoped.prep.PreparedAt),
		Host:            Sanitize(host),
		Sessions:        scoped.rows,
		IndexedEvents:   scoped.indexed,
		FrontierRecords: scoped.frontierRecords,
		SalientTerms:    sanitizeAll(scoped.terms),
		Related:         scoped.related,
		Serendipitous:   scoped.prep.Serendipitous,
		Database:        Sanitize(runs.Path()),
		Index:           Sanitize(scoped.index),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	table := make([][]string, 0, len(scoped.rows))
	for _, row := range scoped.rows {
		table = append(table, []string{row.Selector, fmt.Sprintf("%d", row.Bytes), fmt.Sprintf("%d", row.Events)})
	}
	if err := writeTable(a.stdout, []string{"SELECTOR", "BYTES", "EVENTS"}, table); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "\npreparation %s over %d %s\n", res.PreparationID,
		len(scoped.rows), plural(len(scoped.rows), "session", "sessions"))
	fmt.Fprintf(a.stdout, "explore it with: babel explore --preparation %s\n", res.PreparationID)
	if len(res.Related) > 0 {
		framing := "to refine, revive, or amend rather than duplicate"
		if res.Serendipitous {
			framing = "as inspiration, not constraint"
		}
		fmt.Fprintf(a.stdout, "\n%d related prior %s, %s:\n",
			len(res.Related), plural(len(res.Related), "output", "outputs"), framing)
		for _, row := range res.Related {
			fmt.Fprintf(a.stdout, "  %s %s  %s\n", row.Kind, row.ID, row.Summary)
		}
	}
	return nil
}

// scopedCorpus is what fixing one exploration's corpus scope produced: the
// immutable preparation record, the per-session rows a caller reports, how many
// events reached the retrieval index, and where that index lives.
type scopedCorpus struct {
	prep            runstore.Preparation
	rows            []preparedRow
	indexed         int
	frontierRecords int
	terms           []string
	related         []relatedRow
	index           string
}

// fixScope digests and indexes the chosen sessions, derives the preparation
// identity from their content, and records it.
//
// It is the whole of §6.5's preparation step, shared by `babel prepare` and by a
// conductor cycle. Sharing it is not tidiness: a preparation's identity is
// derived from what was selected and digested, so a second implementation that
// digested or normalized differently would mint a different identity for the
// same corpus and make two runs over one scope look like runs over two.
//
// Storing is part of it. A preparation is content-addressed and idempotent
// (§8), so a repeated scope is the same record rather than a second one, and a
// caller that fixed a scope without recording it would hold an identity nothing
// could later resolve.
func (a *app) fixScope(ctx context.Context, runs *runstore.Store,
	chosen []localSession, host string, serendipitous bool) (scopedCorpus, error) {
	d, err := babelDirs()
	if err != nil {
		return scopedCorpus{}, err
	}
	idx, err := index.Open(d.indexDir())
	if err != nil {
		return scopedCorpus{}, err
	}
	defer idx.Close()

	version := readBuildIdentity().Version
	out := scopedCorpus{
		rows:  make([]preparedRow, 0, len(chosen)),
		index: idx.Path(),
	}
	selection := make([]runstore.Selected, 0, len(chosen))
	// The scope's salient terms are accumulated during the digest pass, which
	// already reads every event of every selected session. Deriving them
	// there rather than in a third pass is what keeps the frontier query free
	// on a corpus whose only real cost is bytes read.
	salience := index.NewSalience()
	for n, s := range chosen {
		// A cold preparation reads every selected session twice, once to
		// digest and once to index, which takes minutes on a large corpus:
		// it narrates on stderr so the wait is never silent, while stdout
		// keeps carrying exactly one document.
		a.diagf("preparing %d/%d %s...\n", n+1, len(chosen), Sanitize(s.key()))
		desc, err := describe(ctx, s)
		if err != nil {
			return scopedCorpus{}, err
		}
		stream := event.Stream{
			Harness:       s.src.Harness,
			AdapterSchema: s.owner.Schema(),
			SourceID:      s.src.SourceID,
			Path:          s.src.PrimaryPath,
		}
		capture, source, size, err := streamDigests(stream, salience)
		if err != nil {
			return scopedCorpus{}, err
		}
		result, err := idx.IndexSession(ctx, stream)
		if err != nil {
			return scopedCorpus{}, fmt.Errorf("index %s: %w", s.key(), err)
		}
		out.indexed += result.Events
		selection = append(selection, runstore.Selected{
			Host:          host,
			Harness:       s.src.Harness,
			SourceID:      s.src.SourceID,
			CaptureDigest: capture,
			SourceDigest:  source,
			Adapter: runstore.AdapterRef{
				Schema:       s.owner.Schema(),
				Version:      version,
				Completeness: completenessOf(desc),
			},
		})
		out.rows = append(out.rows, preparedRow{
			Harness:       Sanitize(s.src.Harness),
			SourceID:      Sanitize(s.src.SourceID),
			Selector:      Sanitize(s.key()),
			Host:          Sanitize(host),
			CaptureDigest: string(capture),
			SourceDigest:  string(source),
			Bytes:         size,
			Records:       result.Records,
			Events:        result.Events,
		})
	}

	// The frontier's own retrieval surface is reconciled here, with the
	// sessions, because this is the command that fixes a scope: the related
	// outputs a preparation records have to be the ones the frontier held
	// when it was fixed, not the ones it held whenever some earlier command
	// last looked.
	front, err := frontier.Open(d.durableDir())
	if err != nil {
		return scopedCorpus{}, err
	}
	defer front.Close()
	// The operator's complaints are reconciled in the same pass, which is what
	// makes them steering rather than a suggestion box (#115): a preparation
	// that surfaced Babel's own prior output and not the operator's standing
	// annoyances would relate this scope to everything except the thing a
	// person actually said about it.
	told, err := complaint.Open(d.durableDir())
	if err != nil {
		return scopedCorpus{}, err
	}
	defer told.Close()
	terms := salience.Terms(0)
	frontierRecords, related, err := a.relatedOutputs(ctx, idx, front, told, terms)
	if err != nil {
		return scopedCorpus{}, err
	}
	refs := make([]runstore.RelatedOutput, 0, len(related))
	for _, row := range related {
		refs = append(refs, runstore.RelatedOutput{Kind: row.Kind, ID: row.ID})
	}
	out.frontierRecords = frontierRecords
	out.terms = terms
	out.related = related

	prep, err := runstore.NewPreparation(time.Now().UTC(), selection, runstore.PreparationContext{
		Related:       refs,
		Serendipitous: serendipitous,
	})
	if err != nil {
		return scopedCorpus{}, fmt.Errorf("fix the corpus scope: %w", err)
	}
	if err := runs.PutPreparation(ctx, prep); err != nil {
		return scopedCorpus{}, fmt.Errorf("record preparation %s: %w", prep.ID, err)
	}
	out.prep = prep
	return out, nil
}

// selectSessions narrows a scan to the named selectors, or to everything
// when none were given. A selector that matches nothing is a rejected
// invocation rather than a silently smaller scope, because a preparation
// records what an operator meant to explore.
func selectSessions(c *cmd, sessions []localSession, selectors []string) ([]localSession, error) {
	if len(selectors) == 0 {
		if len(sessions) == 0 {
			return nil, fmt.Errorf("no local sessions to prepare")
		}
		return sessions, nil
	}
	var chosen []localSession
	seen := make(map[string]struct{}, len(selectors))
	for _, selector := range selectors {
		s, err := resolveSelector(c, sessions, selector)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[s.key()]; dup {
			continue
		}
		seen[s.key()] = struct{}{}
		chosen = append(chosen, s)
	}
	return chosen, nil
}

// relatedOutputs reconciles the frontier surface of the retrieval index and
// asks it, with the scope's own salient terms, what Babel has already said
// about material like this (#87 item 4).
//
// It is mechanical from end to end: the terms come from term frequency against
// document frequency over the prepared sessions, the query is the same
// optional-term FTS expression a worker's search would use, and the page is
// bounded by run.MaxRelatedOutputs. No model is consulted and none could be —
// this runs inside `babel prepare`, which sends nothing anywhere.
//
// A scope with nothing searchable in it, or a frontier holding nothing, yields
// no related records and no error. That is a real answer: the first
// preparation on a machine has no prior output to relate to, and a preparation
// that pretended otherwise would be recording an empty gesture in an immutable
// record.
func (a *app) relatedOutputs(ctx context.Context, idx *index.Index, front *frontier.Store,
	told *complaint.Store, terms []string) (int, []relatedRow, error) {
	outputs, err := front.Outputs(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read the frontier: %w", err)
	}
	// One reconcile over both sources, because IndexFrontier deletes the local
	// rows the set does not name: a frontier-only pass here would delete every
	// complaint capture indexed, and the next `babel tell` would put them back.
	outputs, err = complaint.Append(ctx, told, outputs)
	if err != nil {
		return 0, nil, err
	}
	indexed, err := idx.IndexFrontier(ctx, outputs)
	if err != nil {
		return 0, nil, fmt.Errorf("index the frontier: %w", err)
	}
	if len(terms) == 0 || indexed.Records == 0 {
		return indexed.Records, nil, nil
	}
	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{
		Match: strings.Join(terms, " "),
		Order: index.OrderRelevance,
		Limit: runstore.MaxRelatedOutputs,
	})
	if err != nil {
		// An unsearchable query is not a failed preparation. The scope
		// produced no term the tokenizer could match, which is a fact about
		// the scope, and the preparation records no related outputs.
		if errors.Is(err, index.ErrNoSearchableTerm) || errors.Is(err, index.ErrMatchTooLong) {
			return indexed.Records, nil, nil
		}
		return 0, nil, fmt.Errorf("search the frontier: %w", err)
	}
	related := make([]relatedRow, 0, len(hits))
	for _, hit := range hits {
		related = append(related, relatedRow{
			Kind:    Sanitize(string(hit.Kind)),
			ID:      Sanitize(hit.ID),
			Summary: Sanitize(hit.Summary),
			Status:  Sanitize(string(hit.Status)),
		})
	}
	return indexed.Records, related, nil
}

// streamDigests reads one primary log once and returns both digests §7
// requires of a selection entry, plus the bytes read. It also feeds the
// scope's salience accumulator, when one is supplied.
//
// The capture digest covers the file as it is on disk, which is what a
// restore is checked against. The source digest covers the normalized event
// stream, which is what analysis actually reads: a normalization change
// moves the second without moving the first, and that difference is what a
// later reviewer needs in order to tell "the corpus changed" from "our
// reading of it changed". Both come from one pass, because the corpus is
// dominated by a handful of very large sessions and reading them twice per
// preparation would double the only cost that matters — which is also why
// the salient terms are counted here instead of in a pass of their own.
func streamDigests(stream event.Stream, salience *index.Salience) (capture, source digest.Digest, size int64, err error) {
	file, err := os.Open(stream.Path)
	if err != nil {
		return "", "", 0, fmt.Errorf("open %s: %w", stream.Path, err)
	}
	defer file.Close()

	captureHash := sha256.New()
	sourceHash := sha256.New()
	encoder := json.NewEncoder(sourceHash)
	counted := &countingReader{r: io.TeeReader(file, captureHash)}
	if err := event.Scan(counted, stream, func(e event.Event) error {
		if salience != nil {
			salience.Add(e.Text)
		}
		return encoder.Encode(e)
	}); err != nil {
		return "", "", 0, fmt.Errorf("read %s: %w", stream.Path, err)
	}
	// Whatever the scanner did not consume still belongs to the capture: the
	// capture digest identifies the file, not the part of it that parsed.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return "", "", 0, fmt.Errorf("read %s: %w", stream.Path, err)
	}
	return digest.New([sha256.Size]byte(captureHash.Sum(nil))),
		digest.New([sha256.Size]byte(sourceHash.Sum(nil))),
		counted.n, nil
}

// countingReader counts the bytes read through it, which is how the capture
// size is learned without a second stat that could disagree with what was
// hashed.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// completenessOf is the adapter's account of the metadata it could not
// derive. It is copied into the preparation because §3 forbids synthesizing
// a value to satisfy a shape, and a scope record that hid the gaps would be
// claiming a fidelity the adapter did not report.
func completenessOf(desc *adapter.Description) []adapter.CompletenessReason {
	if desc == nil {
		return nil
	}
	return desc.Meta.Completeness
}
