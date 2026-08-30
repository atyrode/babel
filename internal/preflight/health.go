package preflight

import (
	"fmt"
	"os"

	"github.com/atyrode/babel/internal/event"
)

// Thresholds are the limits the size and volume checks apply. They are part
// of the report so that a report explains its own verdicts, and they are
// overridable because a corpus with a different shape deserves different
// limits rather than a fork of this package.
type Thresholds struct {
	// TranscriptBytes is the primary-log size above which an input is
	// reported as oversize.
	TranscriptBytes int64 `json:"transcript_bytes"`
	// RecordBytes is the single-record size above which a record is
	// reported as oversize.
	RecordBytes int64 `json:"record_bytes"`
	// AttachmentBytes is the artifact size above which an attachment is
	// reported as oversize.
	AttachmentBytes int64 `json:"attachment_bytes"`
	// MaxFindingsPerInput caps the distinct findings one input may
	// contribute.
	MaxFindingsPerInput int `json:"max_findings_per_input"`
	// EntropyMinLength and EntropyMinBits tune the unstructured-secret
	// heuristic: the shortest candidate worth judging and the Shannon
	// entropy per character, in bits, it must reach.
	EntropyMinLength int     `json:"entropy_min_length"`
	EntropyMinBits   float64 `json:"entropy_min_bits"`
}

// DefaultThresholds returns the calibrated limits.
//
// Every number here is chosen against the measured shape of a real archived
// corpus rather than for looking round, and the shape is what matters: the
// distribution of both session sizes and record sizes is extremely skewed.
// Almost every session is small, a few are two to three orders of magnitude
// larger than the median, and a small fraction of files holds a large share of
// all bytes. A limit set near the middle of such a distribution fires on
// thousands of ordinary sessions and tells an operator nothing.
//
//   - TranscriptBytes is 64 MiB. Ordinary sessions are far below it — the
//     median is under a megabyte — while the largest observed primary logs are
//     several times above it. It therefore names the handful of sessions that
//     dominate the cost of a run, which is exactly the set worth deciding
//     about before inference, and stays silent on the merely large ones.
//
//   - RecordBytes is 1 MiB. Records cluster in the low kilobytes; only about
//     one record in two thousand exceeds a megabyte, and those are embedded
//     payloads, whole-file reads, and large tool results rather than
//     conversation. Above this an input needs chunking attention. It sits well
//     under internal/event's 16 MiB record budget, so a record can be reported
//     as unusual long before the scanner has to degrade it.
//
//   - AttachmentBytes is 16 MiB, one order of magnitude above the ordinary
//     artifact and at the scale where an attachment is a binary payload rather
//     than a note or a diff.
//
//   - MaxFindingsPerInput is 256. It exists because the top percentile of
//     files holds a large share of all bytes: without a cap, one pathological
//     session decides the size of the whole report. 256 distinct findings is
//     already more than a reviewer will read for one session, and the
//     omissions are counted in Stats.
//
//   - EntropyMinLength is 24 and EntropyMinBits is 3.5. Every credential
//     format worth catching that has no recognizable structure is longer than
//     24 characters, and 3.5 bits per character is above prose and identifiers
//     in the same alphabet while below random base64, which sits near 6.
func DefaultThresholds() Thresholds {
	return Thresholds{
		TranscriptBytes:     64 << 20,
		RecordBytes:         1 << 20,
		AttachmentBytes:     16 << 20,
		MaxFindingsPerInput: 256,
		EntropyMinLength:    24,
		EntropyMinBits:      3.5,
	}
}

// validate rejects thresholds that would make the preflight lie: a
// non-positive size limit fires on everything, and an entropy floor outside
// the possible range of Shannon entropy over bytes either fires on everything
// or on nothing.
func (t Thresholds) validate() error {
	switch {
	case t.TranscriptBytes <= 0:
		return fmt.Errorf("preflight: transcript byte limit %d must be positive", t.TranscriptBytes)
	case t.RecordBytes <= 0:
		return fmt.Errorf("preflight: record byte limit %d must be positive", t.RecordBytes)
	case t.AttachmentBytes <= 0:
		return fmt.Errorf("preflight: attachment byte limit %d must be positive", t.AttachmentBytes)
	case t.MaxFindingsPerInput <= 0:
		return fmt.Errorf("preflight: finding cap %d must be positive", t.MaxFindingsPerInput)
	case t.EntropyMinLength < 8:
		return fmt.Errorf("preflight: entropy candidate length %d is short enough to match ordinary words", t.EntropyMinLength)
	case t.EntropyMinBits <= 0 || t.EntropyMinBits > 8:
		return fmt.Errorf("preflight: entropy floor %v bits is outside the range of byte entropy", t.EntropyMinBits)
	}
	return nil
}

// findingKey deduplicates. Two conditions of the same category and detector
// in the same input, about the same value or reference, are one finding that
// recurred rather than two findings.
type findingKey struct {
	sourceID    string
	category    Category
	detector    string
	placeholder string
	reference   string
}

// checker accumulates one report. It holds no corpus bytes: the per-input
// state is a record cursor and the dedupe index.
type checker struct {
	th       Thresholds
	findings []Finding
	index    map[findingKey]int
	perInput map[string]int
	stats    Stats

	// priorBySID and priorDig index the prior preparation by source id and by
	// digest: the first answers "did this input change", the second answers
	// "was this content already prepared under another identity".
	priorBySID map[string]string
	priorDig   map[string]string
	// digests maps a content digest to the first source id in this request
	// that carried it.
	digests map[string]string
}

// add stores a finding, merging it into an existing one when the same
// condition recurs, and drops it when the input's cap is reached. The
// distinct-secret counters live here because "distinct" is exactly what
// survives deduplication.
func (c *checker) add(f Finding) {
	sid := f.Evidence.SourceID()
	key := findingKey{sid, f.Category, f.Detector, f.Placeholder, f.Reference}
	if i, ok := c.index[key]; ok {
		c.findings[i].Occurrences += f.Occurrences
		if f.Measure > c.findings[i].Measure {
			c.findings[i].Measure = f.Measure
		}
		return
	}
	if c.perInput[sid] >= c.th.MaxFindingsPerInput {
		c.stats.FindingsOmitted++
		return
	}
	c.index[key] = len(c.findings)
	c.perInput[sid]++
	c.findings = append(c.findings, f)
	if f.Category == CategorySecret {
		c.stats.SecretFindings++
		switch f.Confidence {
		case ConfidenceStructural:
			c.stats.StructuralSecretFindings++
		case ConfidenceHeuristic:
			c.stats.HeuristicSecretFindings++
		}
	}
}

// check runs every check for one input.
func (c *checker) check(in Input) error {
	if err := validateInput(in); err != nil {
		return err
	}
	c.stats.Inputs++
	ev, err := c.inputEvidence(in)
	if err != nil {
		return err
	}
	c.provenance(in, ev)
	c.attachments(in, ev)
	c.references(in, ev)
	return c.scan(in, ev)
}

// validateInput rejects an input preflight cannot produce recoverable
// evidence about. Refusing is the point: an input silently skipped would be
// reported as a clean input that was never read.
func validateInput(in Input) error {
	switch {
	case in.Stream.Harness == "":
		return fmt.Errorf("preflight: input without a harness")
	case in.Stream.SourceID == "":
		return fmt.Errorf("preflight: input %s without a source id", in.Stream.Path)
	case in.Stream.Path == "":
		return fmt.Errorf("preflight: input %s without a primary log path", in.Stream.SourceID)
	case in.Digest == "":
		return fmt.Errorf("preflight: input %s without a primary log digest", in.Stream.SourceID)
	}
	return nil
}

// inputEvidence is the locator for a claim about a whole input: its primary
// log and that log's digest.
func (c *checker) inputEvidence(in Input) (Evidence, error) {
	return NewEvidence(in.Stream.Harness, in.Stream.SourceID, WholeInput, "",
		event.Locator{Path: in.Stream.Path, Digest: in.Digest})
}

// provenance answers §6.4's duplicate-or-changed question. Both answers are
// about identity rather than content, so both are ConfidenceObserved: a digest
// that differs from the recorded one is not a judgement.
func (c *checker) provenance(in Input, ev Evidence) {
	switch prior, known := c.priorBySID[in.Stream.SourceID]; {
	case !known:
		c.stats.NewInputs++
	case prior == in.Digest:
		c.stats.UnchangedInputs++
	default:
		c.stats.ChangedInputs++
		c.add(Finding{
			Category:   CategoryChangedInput,
			Detector:   "input-provenance",
			Confidence: ConfidenceObserved,
			Summary:    "the input's primary log digest differs from the one a prior preparation recorded for the same session",
			Reference:  prior,
			// A grown session is the normal case: harness logs are
			// append-mostly, so "changed" means re-index rather than
			// suspect.
			Occurrences: 1,
			Evidence:    ev,
		})
	}

	other, duplicate := c.digests[in.Digest]
	if !duplicate {
		other, duplicate = c.priorDig[in.Digest]
		if duplicate && other == in.Stream.SourceID {
			duplicate = false
		}
	}
	if _, seen := c.digests[in.Digest]; !seen {
		c.digests[in.Digest] = in.Stream.SourceID
	}
	if !duplicate {
		return
	}
	c.stats.DuplicateInputs++
	c.add(Finding{
		Category:    CategoryDuplicateInput,
		Detector:    "input-provenance",
		Confidence:  ConfidenceObserved,
		Summary:     "two inputs have identical primary-log digests, so analysing both would double-count the same evidence",
		Reference:   other,
		Occurrences: 1,
		Evidence:    ev,
	})
}

// attachments checks artifact metadata and size. It never opens an artifact:
// closure and content digesting belong to the adapter that discovered it, and
// hashing every artifact to report that one of them is large would make this
// check cost a full pass over the closure.
func (c *checker) attachments(in Input, inputEv Evidence) {
	for _, a := range in.Attachments {
		c.stats.Attachments++
		c.stats.AttachmentBytes += a.Size

		ev, ref := c.attachmentEvidence(in, a, inputEv)
		if a.Size > c.th.AttachmentBytes {
			c.stats.OversizeAttachments++
			c.add(Finding{
				Category:    CategoryOversizeAttachment,
				Detector:    "attachment-size",
				Confidence:  ConfidenceObserved,
				Summary:     "an attachment is larger than the attachment review limit",
				Measure:     a.Size,
				Limit:       c.th.AttachmentBytes,
				Reference:   ref,
				Occurrences: 1,
				Evidence:    ev,
			})
		}
		// An artifact's path is metadata this preflight can read without
		// opening the file, and a path is a real place for a credential to
		// end up: a downloaded key, a token in a query string saved as a
		// filename.
		c.secretsIn(a.Path, ev, ref, true)
	}
}

// attachmentEvidence prefers the artifact's own locator and falls back to the
// session's, because an adapter reports artifact paths and sizes without
// hashing them. The fallback keeps the evidence recoverable — the session's
// digest still proves what was read — and names the artifact in the finding's
// Reference rather than inventing a digest for it.
func (c *checker) attachmentEvidence(in Input, a Attachment, inputEv Evidence) (Evidence, string) {
	if a.Digest != "" && a.Path != "" {
		ev, err := NewEvidence(in.Stream.Harness, in.Stream.SourceID, WholeInput, "",
			event.Locator{Path: a.Path, Digest: a.Digest})
		if err == nil {
			return ev, a.Path
		}
	}
	return inputEv, a.Path
}

// references reports the closure gaps the adapter found. §2.5 and §3 make an
// unresolved reference a completeness fact rather than an error: the
// transcript is still analysable, and the run should know its evidence is
// incomplete before it starts.
func (c *checker) references(in Input, ev Evidence) {
	for _, r := range in.Unresolved {
		c.stats.UnresolvedReferences++
		summary := "the adapter could not resolve a referenced artifact or blob, so this input's evidence closure is incomplete"
		if r.Reason != "" {
			summary += ": " + r.Reason
		}
		c.add(Finding{
			Category:    CategoryUnresolvedReference,
			Detector:    "closure",
			Confidence:  ConfidenceObserved,
			Summary:     summary,
			Reference:   r.Ref,
			Occurrences: 1,
			Evidence:    ev,
		})
	}
}

// record is one record's worth of per-input scan state, kept so a record can
// be classified once its length is known — which is only when the next record
// starts, or when the file ends.
type record struct {
	line    int
	offset  int64
	index   int
	kind    event.Kind
	digest  string
	partial bool
}

// scan streams the primary log once. Everything per-record — size, integrity,
// secrets in event text — is decided from this single pass, so the cost of the
// preflight is proportional to the corpus's bytes and not to a re-read per
// check.
func (c *checker) scan(in Input, ev Evidence) error {
	f, err := os.Open(in.Stream.Path)
	if err != nil {
		return fmt.Errorf("preflight: open %s: %w", in.Stream.SourceID, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("preflight: stat %s: %w", in.Stream.SourceID, err)
	}
	size := info.Size()
	c.stats.Bytes += size

	if size > c.th.TranscriptBytes {
		c.stats.OversizeTranscripts++
		c.add(Finding{
			Category:    CategoryOversizeTranscript,
			Detector:    "transcript-size",
			Confidence:  ConfidenceObserved,
			Summary:     "the primary log is larger than the transcript review limit, so this input dominates the run's cost and needs a chunking decision",
			Measure:     size,
			Limit:       c.th.TranscriptBytes,
			Occurrences: 1,
			Evidence:    ev,
		})
	}

	// A log whose final byte is not a newline was captured mid-write (§6.1):
	// crash-consistent per file means a prefix plus at most a torn final
	// record. That is the only way to tell a torn final record from a
	// malformed one in the middle, and the two mean different things — one is
	// a capture race the next snapshot supersedes, the other is corruption.
	terminated, err := endsWithNewline(f, size)
	if err != nil {
		return fmt.Errorf("preflight: read %s: %w", in.Stream.SourceID, err)
	}

	var cur record
	scanErr := event.Scan(f, in.Stream, func(e event.Event) error {
		c.stats.Events++
		if e.Locator.Line != cur.line {
			if cur.line != 0 {
				if err := c.classify(in, cur, e.Locator.ByteOffset-cur.offset, false, terminated); err != nil {
					return err
				}
			}
			cur = record{
				line:    e.Locator.Line,
				offset:  e.Locator.ByteOffset,
				index:   e.Index,
				kind:    e.Kind,
				digest:  e.Locator.Digest,
				partial: e.Partial,
			}
		}
		ev, err := NewEvidence(in.Stream.Harness, in.Stream.SourceID, e.Index, e.Kind, e.Locator)
		if err != nil {
			return err
		}
		c.secretsIn(e.Text, ev, "", false)
		return nil
	})
	if scanErr != nil {
		return fmt.Errorf("preflight: scan %s: %w", in.Stream.SourceID, scanErr)
	}
	if cur.line == 0 {
		return nil
	}
	return c.classify(in, cur, size-cur.offset, true, terminated)
}

// endsWithNewline reports whether the log's last byte terminates its final
// record. An empty file is treated as terminated: it has no torn record.
func endsWithNewline(f *os.File, size int64) (bool, error) {
	if size == 0 {
		return true, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return false, err
	}
	return last[0] == '\n', nil
}

// classify accounts for one record: its size against the record limit, and
// its integrity when internal/event marked it partial.
//
// A partial record has three possible causes and they are not
// interchangeable. Past internal/event's record budget, "partial" is the
// scanner's own truncation rather than damage. A partial final record in a log
// with no terminating newline is a capture taken mid-write. Anything else is a
// record that is not valid JSON where the file says one should be.
func (c *checker) classify(in Input, rec record, length int64, last, terminated bool) error {
	c.stats.Records++
	ev, err := NewEvidence(in.Stream.Harness, in.Stream.SourceID, rec.index, rec.kind,
		event.Locator{Path: in.Stream.Path, Line: rec.line, ByteOffset: rec.offset, Digest: rec.digest})
	if err != nil {
		return err
	}
	if length > c.th.RecordBytes {
		c.stats.OversizeRecords++
		c.add(Finding{
			Category:    CategoryOversizeRecord,
			Detector:    "record-size",
			Confidence:  ConfidenceObserved,
			Summary:     "a single record is larger than the record review limit, which is an embedded payload or a whole-file result rather than conversation",
			Measure:     length,
			Limit:       c.th.RecordBytes,
			Occurrences: 1,
			Evidence:    ev,
		})
	}
	if !rec.partial || length > event.MaxRecordBytes {
		return nil
	}
	if last && !terminated {
		c.stats.TruncatedInputs++
		c.add(Finding{
			Category:    CategoryTruncatedInput,
			Detector:    "record-integrity",
			Confidence:  ConfidenceObserved,
			Summary:     "the final record is truncated and the log has no terminating newline, which is a capture taken while the harness was still appending",
			Occurrences: 1,
			Evidence:    ev,
		})
		return nil
	}
	c.stats.MalformedRecords++
	c.add(Finding{
		Category:    CategoryMalformedRecord,
		Detector:    "record-integrity",
		Confidence:  ConfidenceObserved,
		Summary:     "a record is not valid JSON, so it is retained as opaque evidence and cannot be classified",
		Occurrences: 1,
		Evidence:    ev,
	})
	return nil
}
