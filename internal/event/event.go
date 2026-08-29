// Package event normalizes harness session logs into the common analysis
// event model of SPEC.md §4.1 and §6.3. It is the semantic layer that
// internal/transcript deliberately lacks: transcript renders a session for
// display, while this package classifies each record into the five evidence
// categories §6.3 distinguishes — user reports, agent claims, tool
// observations, repository changes, and verification evidence — and attaches
// a locator that recovers the original record bytes from the archive.
//
// Two properties are load-bearing. Nothing is ever dropped: a malformed
// record, an unrecognized record type, or a record from a harness whose
// format has drifted becomes an opaque event with a valid locator, because
// §6.3 requires explicit degradation rather than silence. And scanning is
// bounded: one reusable record buffer serves the whole stream, so a session
// log scans in memory proportional to its largest record, not to its size.
// The design target is a single primary log of a third of a gigabyte, which
// real harness sessions reach.
package event

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is the analysis category of an event. The five evidence kinds are
// §6.3's common vocabulary; KindOpaque is the explicit degradation channel
// for everything a versioned adapter cannot defend a category for.
type Kind string

// The analysis event kinds. KindOpaque is never a parse failure that was
// hidden: it records that this exact record was seen and could not be
// classified.
const (
	KindUserReport           Kind = "user-report"
	KindAgentClaim           Kind = "agent-claim"
	KindToolObservation      Kind = "tool-observation"
	KindRepositoryChange     Kind = "repository-change"
	KindVerificationEvidence Kind = "verification-evidence"
	KindOpaque               Kind = "opaque"
)

// Outcome values for verification evidence. They are set only from an
// explicit status the harness itself recorded; a harness that does not
// record an exit status leaves Outcome empty rather than having one guessed
// for it.
const (
	OutcomePass  = "pass"
	OutcomeFail  = "fail"
	OutcomeError = "error"
)

// Locator recovers the original evidence. Every event has one, including
// opaque events, so degraded records remain auditable against the archive.
type Locator struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`        // 1-based record line
	ByteOffset int64  `json:"byte_offset"` // offset of the record's first byte
	Digest     string `json:"digest"`      // sha256 of the record bytes, hex
}

// Event is one classified record, or one classified part of a record: a
// harness record that carries both assistant prose and a tool call yields
// one event per semantic unit, all sharing the record's locator.
type Event struct {
	Index   int        `json:"index"`
	Kind    Kind       `json:"kind"`
	Role    string     `json:"role"` // as the harness reported it
	Time    *time.Time `json:"time"`
	Text    string     `json:"text"`
	Tool    string     `json:"tool"`    // tool-observation, verification
	Paths   []string   `json:"paths"`   // repository-change
	Outcome string     `json:"outcome"` // pass|fail|error, verification
	Partial bool       `json:"partial"` // torn or malformed record
	Locator Locator    `json:"locator"`
}

// Stream carries session identity once rather than per event, because
// identity is constant for a primary log and repeating it per event would
// dominate the event's own size.
type Stream struct {
	Harness       string
	AdapterSchema int
	SourceID      string
	Path          string
}

// Harness names, matching internal/adapter's SourceSession.Harness values.
const (
	HarnessOMP    = "omp"
	HarnessCodex  = "codex"
	HarnessClaude = "claude"
)

const (
	// MaxRecordBytes is the line budget: the bytes retained for one
	// record. A longer record is reported as an oversized opaque event
	// with Partial set and scanning continues, so an untrusted log cannot
	// grow the process without bound. Its digest still covers every byte
	// of the record, because the hash is fed the dropped tail as it is
	// drained.
	//
	// The number is 16 MiB rather than a comfortable-looking megabyte
	// because real harness logs are observed to carry single records into
	// the tens of megabytes: one embedded image, one whole-file read, or
	// one large tool output produces a record orders of magnitude past the
	// typical record of about a kilobyte, and the largest single records
	// seen in practice sit in the low tens of MiB. A budget that clears
	// those with headroom keeps the oversized path for genuinely
	// pathological records instead of turning ordinary large tool output
	// into opaque events. The default 64 KiB budget of a plain
	// bufio.Scanner would degrade roughly one record in a hundred.
	MaxRecordBytes = 16 << 20

	// TextRuneLimit bounds Event.Text. Text is a normalized excerpt for
	// analysis, never the authoritative content; Locator recovers the full
	// record.
	TextRuneLimit = 4000

	// commandScanLimit bounds how much of a command line is inspected for
	// verification vocabulary. Commands can embed whole heredocs, and the
	// program being run is always at the front.
	commandScanLimit = 4 << 10

	readBufferBytes = 64 << 10

	// maxPendingCalls bounds the tool calls remembered so that a result
	// record, which usually names only its call id, can be classified with
	// the tool that produced it.
	maxPendingCalls = 256
)

// Scan streams a primary log, calling fn once per classified event in file
// order. It never materializes the whole file.
//
// An unrecognized Stream.Harness is a caller error and fails immediately:
// format drift is a record-level condition handled by degrading to
// KindOpaque, not a reason to accept an unknown adapter name and silently
// return an all-opaque stream. A read error aborts the scan with context;
// an error from fn is returned unwrapped so callers can compare it.
func Scan(r io.Reader, s Stream, fn func(Event) error) error {
	classify := classifierFor(s.Harness)
	if classify == nil {
		return fmt.Errorf("scan %s: unknown harness %q", s.Path, s.Harness)
	}
	sc := &scanner{
		br:       bufio.NewReaderSize(r, readBufferBytes),
		stream:   s,
		hash:     sha256.New(),
		fn:       fn,
		classify: classify,
	}
	sc.calls.byID = make(map[string]pendingCall)
	return sc.run()
}

// classifier classifies one record of a specific harness. Returning false
// means "not mine": the record is emitted as opaque instead.
type classifier func(sc *scanner, rec []byte) (bool, error)

func classifierFor(harness string) classifier {
	switch harness {
	case HarnessOMP:
		return classifyOMP
	case HarnessCodex:
		return classifyCodex
	case HarnessClaude:
		return classifyClaude
	default:
		return nil
	}
}

// scanner holds the whole per-stream scan state. Everything reusable lives
// here so the per-record cost is the record's own decoding, not fresh
// buffers and hashers.
type scanner struct {
	br       *bufio.Reader
	stream   Stream
	hash     hash.Hash
	fn       func(Event) error
	classify classifier

	buf   []byte // reused record buffer, never grown past MaxRecordBytes
	sum   [sha256.Size]byte
	hex   [2 * sha256.Size]byte
	calls callTracker

	index  int
	line   int
	offset int64
	loc    Locator
}

func (s *scanner) run() error {
	for {
		start := s.offset
		oversized, present, readErr := s.readRecord()
		if present {
			s.line++
			s.loc = Locator{Path: s.stream.Path, Line: s.line, ByteOffset: start, Digest: s.recordDigest()}
			if err := s.dispatch(oversized); err != nil {
				return err
			}
		}
		switch readErr {
		case nil:
			continue
		case io.EOF:
			return nil
		default:
			return fmt.Errorf("scan %s line %d: %w", s.stream.Path, s.line, readErr)
		}
	}
}

// dispatch applies the harness classifier and degrades explicitly when it
// declines or, defensively, when it claims a record but emits nothing: the
// "nothing is dropped" invariant is checked here rather than trusted to
// every classification rule.
//
// An oversized record is never parsed: its retained prefix is not the
// record, so classifying it would classify a fragment.
func (s *scanner) dispatch(oversized bool) error {
	if oversized {
		return s.push(Event{Kind: KindOpaque, Text: clip(s.buf), Partial: true})
	}
	before := s.index
	handled, err := s.classify(s, s.buf)
	if err != nil {
		return err
	}
	if handled && s.index > before {
		return nil
	}
	return s.pushOpaque()
}

// pushOpaque emits the current record as opaque. Partial is set only when
// the record is not valid JSON, which is the observable signature of a torn
// or corrupt line; a well-formed record of an unknown type is intact
// evidence Babel simply cannot categorize yet.
func (s *scanner) pushOpaque() error {
	return s.push(Event{Kind: KindOpaque, Text: clip(s.buf), Partial: !json.Valid(s.buf)})
}

// push completes an event with the fields only the scanner knows and hands
// it to the callback. The event's Text and Paths are the only heap the
// callback may retain; the record buffer is reused immediately after.
func (s *scanner) push(e Event) error {
	e.Index = s.index
	e.Locator = s.loc
	s.index++
	return s.fn(e)
}

// readRecord drains one logical line. It hashes every byte of the record,
// including bytes past MaxRecordBytes that are not retained, so an
// oversized record's digest still identifies the whole record. present is
// false only at a clean end of file.
func (s *scanner) readRecord() (oversized, present bool, err error) {
	s.buf = s.buf[:0]
	s.hash.Reset()
	carriage := false // a '\r' held back until we know it is not a CRLF
	for {
		chunk, readErr := s.br.ReadSlice('\n')
		s.offset += int64(len(chunk))
		if len(chunk) > 0 {
			present = true
		}
		final := readErr != bufio.ErrBufferFull
		body := chunk
		terminated := false
		if final && len(body) > 0 && body[len(body)-1] == '\n' {
			terminated = true
			body = body[:len(body)-1]
		}
		if carriage {
			// Drop the held byte only when it turned out to be the CR of
			// a CRLF terminator straddling the read buffer.
			if !(final && terminated && len(body) == 0) {
				oversized = s.keep([]byte{'\r'}) || oversized
			}
			carriage = false
		}
		if len(body) > 0 && body[len(body)-1] == '\r' {
			body = body[:len(body)-1]
			carriage = true
		}
		oversized = s.keep(body) || oversized
		if !final {
			continue
		}
		if carriage && !terminated {
			// Trailing CR at end of file is content, not a terminator.
			oversized = s.keep([]byte{'\r'}) || oversized
		}
		return oversized, present, readErr
	}
}

// keep hashes b and retains as much of it as the record budget allows,
// reporting whether anything was dropped.
func (s *scanner) keep(b []byte) (dropped bool) {
	if len(b) == 0 {
		return false
	}
	s.hash.Write(b)
	room := MaxRecordBytes - len(s.buf)
	if room >= len(b) {
		s.buf = append(s.buf, b...)
		return false
	}
	if room > 0 {
		s.buf = append(s.buf, b[:room]...)
	}
	return true
}

func (s *scanner) recordDigest() string {
	s.sum = [sha256.Size]byte{}
	s.hash.Sum(s.sum[:0])
	hex.Encode(s.hex[:], s.sum[:])
	return string(s.hex[:])
}

// pendingCall is what a tool result needs from its call record: the tool
// name, the verification match that only the command line can establish,
// and the argument path for the harnesses whose write results do not repeat
// the file they wrote.
type pendingCall struct {
	tool         string
	verification bool
	path         string
}

// callTracker remembers in-flight tool calls by call id. Every harness
// records a tool result that names its call id but not always its tool, and
// none of them repeats the command line on the result, so the outcome and
// the thing that produced it can only be joined here.
//
// It is bounded by construction: the ring evicts the call recorded
// maxPendingCalls puts ago, so a log with unanswered calls degrades to
// results without a tool name instead of growing memory. Real harnesses
// answer a call within a few records.
type callTracker struct {
	byID map[string]pendingCall
	ring [maxPendingCalls]string
	next int
}

func (t *callTracker) put(id string, c pendingCall) {
	if id == "" {
		return
	}
	if old := t.ring[t.next]; old != "" && old != id {
		delete(t.byID, old)
	}
	t.ring[t.next] = id
	t.next = (t.next + 1) % len(t.ring)
	t.byID[id] = c
}

// take returns and forgets a remembered call. A miss is normal for a log
// captured mid-run or one whose call outran the ring.
func (t *callTracker) take(id string) (pendingCall, bool) {
	if id == "" {
		return pendingCall{}, false
	}
	c, ok := t.byID[id]
	if ok {
		delete(t.byID, id)
	}
	return c, ok
}

// contentPart is the shape every harness uses for structured message
// content, with per-harness part type names.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// partText extracts the concatenated text of the accepted part types, or
// the whole value when the harness wrote content as a bare string. ok is
// false when the value is absent or holds no accepted part, which lets a
// classifier decline rather than emit an empty claim.
func partText(raw json.RawMessage, acceptedTypes ...string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return clipString(direct), true
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var b textBuilder
	for _, part := range parts {
		for _, accepted := range acceptedTypes {
			if part.Type == accepted {
				b.add(part.Text)
				break
			}
		}
	}
	if b.empty() {
		return "", false
	}
	return b.string(), true
}

// textBuilder joins text fragments under the rune budget so a record made
// of many large parts cannot produce an event larger than the budget.
type textBuilder struct {
	b     strings.Builder
	runes int
	any   bool
}

func (t *textBuilder) add(s string) {
	t.any = true
	if t.runes >= TextRuneLimit {
		return
	}
	if t.b.Len() > 0 {
		t.b.WriteByte('\n')
	}
	for i := 0; i < len(s); {
		if t.runes >= TextRuneLimit {
			return
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		t.b.WriteRune(r)
		t.runes++
		i += size
	}
}

func (t *textBuilder) empty() bool    { return !t.any }
func (t *textBuilder) string() string { return t.b.String() }

// clip renders a bounded, valid-UTF-8 excerpt of raw record bytes for an
// opaque event, so a binary or truncated line is still inspectable.
func clip(b []byte) string {
	var t textBuilder
	t.add(string(clipBytes(b)))
	return t.string()
}

// clipBytes bounds the bytes converted to a string before rune clipping, so
// a 4 MiB opaque record does not allocate 4 MiB to then discard it. Four
// bytes per rune is the UTF-8 maximum.
func clipBytes(b []byte) []byte {
	if len(b) > 4*TextRuneLimit {
		return b[:4*TextRuneLimit]
	}
	return b
}

func clipString(s string) string {
	if len(s) <= TextRuneLimit {
		return s
	}
	var t textBuilder
	t.add(s)
	return t.string()
}

// recordTime parses a harness timestamp. An absent or unparseable value
// yields nil: §3 forbids synthesizing a value to satisfy the shape.
func recordTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}
