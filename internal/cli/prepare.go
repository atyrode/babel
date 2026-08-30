package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
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

Nothing is sent anywhere and no repository is opened: this command reads
local files and writes local state only.

Flags:
  --harness NAME       restrict to one harness: omp, codex, or claude
  --roots DIR[,DIR]    scan these roots instead of the adapter defaults
  --host ID            archive host identity recorded in the selection
                       (default $BABEL_HOST_ID, else storage.json, else this
                       machine's hostname)
  --json               emit the preparation as JSON on stdout

A selector is "HARNESS/SOURCE-ID", or any unambiguous suffix of one.
`

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
	Database      string        `json:"database"`
	Index         string        `json:"index"`
}

// prepare implements `babel prepare`.
func (a *app) prepare(ctx context.Context, args []string) error {
	c := newCmd("prepare", prepareUsage)
	var sf scanFlags
	var rf repoFlags
	sf.bindHarness(c)
	sf.bindRoots(c)
	c.fs.StringVar(&rf.host, "host", "", "archive host identity recorded in the selection")
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
	idx, err := index.Open(d.indexDir())
	if err != nil {
		return err
	}
	defer idx.Close()

	version := readBuildIdentity().Version
	rows := make([]preparedRow, 0, len(chosen))
	selection := make([]runstore.Selected, 0, len(chosen))
	indexed := 0
	for n, s := range chosen {
		// A cold preparation reads every selected session twice, once to
		// digest and once to index, which takes minutes on a large corpus:
		// it narrates on stderr so the wait is never silent, while stdout
		// keeps carrying exactly one document.
		a.diagf("preparing %d/%d %s...\n", n+1, len(chosen), Sanitize(s.key()))
		desc, err := describe(ctx, s)
		if err != nil {
			return err
		}
		stream := event.Stream{
			Harness:       s.src.Harness,
			AdapterSchema: s.owner.Schema(),
			SourceID:      s.src.SourceID,
			Path:          s.src.PrimaryPath,
		}
		capture, source, size, err := streamDigests(stream)
		if err != nil {
			return err
		}
		result, err := idx.IndexSession(ctx, stream)
		if err != nil {
			return fmt.Errorf("index %s: %w", s.key(), err)
		}
		indexed += result.Events
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
		rows = append(rows, preparedRow{
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

	prep, err := runstore.NewPreparation(time.Now().UTC(), selection)
	if err != nil {
		return fmt.Errorf("fix the corpus scope: %w", err)
	}
	runs, err := runstore.Open(d.durableDir())
	if err != nil {
		return err
	}
	defer runs.Close()
	if err := runs.PutPreparation(ctx, prep); err != nil {
		return fmt.Errorf("record preparation %s: %w", prep.ID, err)
	}

	res := prepareResult{
		PreparationID: string(prep.ID),
		PreparedAt:    formatTime(prep.PreparedAt),
		Host:          Sanitize(host),
		Sessions:      rows,
		IndexedEvents: indexed,
		Database:      Sanitize(runs.Path()),
		Index:         Sanitize(idx.Path()),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.Selector, fmt.Sprintf("%d", row.Bytes), fmt.Sprintf("%d", row.Events)})
	}
	if err := writeTable(a.stdout, []string{"SELECTOR", "BYTES", "EVENTS"}, table); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "\npreparation %s over %d %s\n", res.PreparationID, len(rows), plural(len(rows), "session", "sessions"))
	fmt.Fprintf(a.stdout, "explore it with: babel explore --preparation %s\n", res.PreparationID)
	return nil
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

// streamDigests reads one primary log once and returns both digests §7
// requires of a selection entry, plus the bytes read.
//
// The capture digest covers the file as it is on disk, which is what a
// restore is checked against. The source digest covers the normalized event
// stream, which is what analysis actually reads: a normalization change
// moves the second without moving the first, and that difference is what a
// later reviewer needs in order to tell "the corpus changed" from "our
// reading of it changed". Both come from one pass, because the corpus is
// dominated by a handful of very large sessions and reading them twice per
// preparation would double the only cost that matters.
func streamDigests(stream event.Stream) (capture, source digest.Digest, size int64, err error) {
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
