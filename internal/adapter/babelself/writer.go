package babelself

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/event"
)

// The record types a log holds. They are OMP's, for the reason the package
// comment gives: these records carry OMP's own message objects, and renaming
// the envelope around them would make the log a format Babel invented for
// content it did not.
const (
	recordTitle    = "title"
	recordSession  = "session"
	recordMessage  = "message"
	recordCustom   = "custom"
	sessionVersion = 1
)

// The annotations Babel writes about its own writing. They travel as
// "custom" records — OMP's channel for a record that is not a message —
// because they are not part of the conversation and a reader that took them
// for one would be reading Babel's bookkeeping as the model's words.
const (
	// annotationCompaction marks a turn whose message list came back shorter
	// than what is already persisted: the engine compacted the
	// conversation, replacing history with a summary.
	annotationCompaction = "babel_compaction"
	// annotationUndecodedTurn marks a turn whose message list did not
	// decode. The gap is recorded; the bytes are not.
	annotationUndecodedTurn = "babel_turn_undecoded"
	// annotationReopened marks the point a resumed attempt of the same run
	// began appending to a log an earlier attempt had opened.
	annotationReopened = "babel_reopened"
)

// The message roles whose content is Babel's own or the model's own:
// respectively the job document Babel composed and the text, reasoning and
// tool calls the model produced. Everything else is content some facility
// served, and reduce() is what happens to it.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// toolResultFields are the fields a reduced message keeps: the identity of
// the call it answers and whether it failed. It is an allowlist rather than
// a denylist of content fields, because the failure mode of the opposite
// choice is silent — a harness that adds one field carrying the result's
// body would have it persisted from then on, and nobody would notice until
// the corpus was already copied.
var toolResultFields = [...]string{"role", "toolCallId", "toolName", "isError", "timestamp"}

// WithheldSchema names the document a reduced tool result carries in place
// of the content a facility served.
const WithheldSchema = "babel.withheld-tool-result/1"

// Redactor reduces one served tool result to what a durable record may hold:
// the locators that recover its bytes from the archive, and the reason the
// content itself is not here.
//
// It is the writer's whole defence of SPEC.md §9 and the reason a Writer
// cannot be built without one. A served corpus excerpt reaches the model
// because a model that cannot read a record cannot form an observation about
// it (internal/explore/retrieval.go serve); written into a session log
// verbatim it would copy the archive into a second plaintext artifact,
// readable by anyone with access to Babel's data directory. So the excerpt
// goes onto the wire and the locator goes into the log — exactly the
// asymmetry run.NewEvidence already implements for the receipt.
//
// The facility behind the capability implements it, because the payload's
// schema belongs to that facility and not to this package. An implementation
// that cannot recognize a payload must return no locators and a reason
// saying so: withholding an unrecognized result is the conservative answer,
// and the alternative is a schema change quietly widening what Babel keeps.
type Redactor func(result string) (locators []event.Locator, reason string)

// StoreConfig is what a Store needs. Everything in it is fixed for the whole
// of one exploration.
type StoreConfig struct {
	// Root is the analysis-session root the logs are written under, which
	// is Root() in every deployment. It is a field rather than a call so a
	// test writes into its own directory.
	Root string

	// Redact is required. A writer with no redactor is not a writer with a
	// permissive policy; it is a path from the archive into a second copy
	// of itself, so NewStore refuses one.
	Redact Redactor

	// Workspace, Profile and Recipes are the run's own facts, recorded in
	// each log's session record so a reader of the log alone can tell what
	// produced it.
	Workspace string
	Profile   string
	Recipes   []string

	// Now is the clock, injectable so a test's records are deterministic.
	// Nil means time.Now.
	Now func() time.Time
}

// Store opens one session log per supervised job of an exploration.
type Store struct {
	cfg StoreConfig
	now func() time.Time
}

// NewStore validates cfg and returns a store. It creates nothing: the root
// appears when the first job opens its log.
func NewStore(cfg StoreConfig) (*Store, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("babelself: no analysis-session root")
	}
	if cfg.Redact == nil {
		return nil, errors.New("babelself: a transcript writer requires a redactor; a served excerpt may not be persisted (SPEC.md §9)")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Store{cfg: cfg, now: now}, nil
}

// Open opens the log for one job of one run at "<root>/<runID>/<job>.jsonl",
// writing its header records the first time and an annotation on every
// later attempt.
//
// The two components are used verbatim rather than sanitized, and an id they
// could not compose is refused here. Sanitizing would mint an identity no
// later reader could reproduce from the run it names, and the caller's own
// identifiers are the ones a reader will search for.
func (s *Store) Open(runID, job string) (*Writer, error) {
	sourceID := runID + "/" + job
	if strings.Contains(runID, "/") || strings.Contains(job, "/") || !adapter.ValidSourceID(sourceID) {
		return nil, fmt.Errorf("babelself: run %q job %q do not compose a source id", runID, job)
	}
	dir := filepath.Join(s.cfg.Root, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("babelself: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, job+sessionExt)
	// O_APPEND rather than O_TRUNC: resuming a run reopens the log of the
	// attempt it continues, and an attempt that overwrote the previous one's
	// reasoning would destroy the record this package exists to keep.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("babelself: open %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("babelself: stat %s: %w", path, err)
	}
	w := &Writer{
		file:      file,
		path:      path,
		sourceID:  sourceID,
		redact:    s.cfg.Redact,
		now:       s.now,
		workspace: s.cfg.Workspace,
		profile:   s.cfg.Profile,
		recipes:   s.cfg.Recipes,
	}
	// Record ids and the parent chain are per attempt, so a reopened log
	// starts a second chain whose first record is the annotation below. That
	// is the honest shape of the thing: two attempts wrote this file, and a
	// reader following the chain can see where the second one began.
	if err := w.head(runID, job, info.Size() > 0); err != nil {
		file.Close()
		return nil, err
	}
	return w, nil
}

// Writer is one job's session log. It is used from the supervision goroutine
// of one run and closed by whoever opened it; nothing about it is safe for
// concurrent use, because a session log is a single conversation and an
// interleaved one would not be a record of anything.
type Writer struct {
	file     *os.File
	path     string
	sourceID string
	redact   Redactor
	now      func() time.Time

	// workspace, profile and recipes are the run's own facts, written into
	// the session record so a reader of the log alone can tell what
	// produced it.
	workspace string
	profile   string
	recipes   []string

	// seq numbers the records this writer wrote and parent chains them, the
	// way OMP's own logs chain records: a reader can follow the order even
	// if the file is concatenated with another.
	seq    int
	parent string

	// written is how many of the turn's messages are already persisted. The
	// engine reports the whole conversation with every agent_end, so the
	// turn's contribution is the suffix beyond this.
	written int
}

// Path is the log's absolute path.
func (w *Writer) Path() string { return w.path }

// SourceID is the identity the adapter's Discover assigns this log.
func (w *Writer) SourceID() string { return w.sourceID }

// head writes the records that open one attempt's contribution: the title
// and session records for a new log, or the note that a resumed attempt
// reopened an existing one.
func (w *Writer) head(runID, job string, reopened bool) error {
	at := w.now().UTC()
	if reopened {
		return w.annotate(annotationReopened,
			"a resumed attempt of run "+runID+" reopened this log; the records below are its own", at)
	}
	title := "babel " + job + " pass of run " + runID
	if err := w.writeRecord(map[string]any{"type": recordTitle, "title": title}); err != nil {
		return err
	}
	return w.writeRecord(map[string]any{
		"type":      recordSession,
		"version":   sessionVersion,
		"id":        w.sourceID,
		"runId":     runID,
		"job":       job,
		"timestamp": at.Format(time.RFC3339Nano),
		"cwd":       w.workspace,
		"title":     title,
		"profile":   w.profile,
		"recipes":   w.recipes,
	})
}

// Turn records one ended turn's messages. The engine reports the whole
// conversation in every agent_end frame, so only the suffix beyond what is
// already persisted is new; at is the instant the turn ended, used for any
// message that carries no timestamp of its own.
func (w *Writer) Turn(messages json.RawMessage, at time.Time) error {
	if w.file == nil {
		return errors.New("babelself: the session log is closed")
	}
	trimmed := bytes.TrimSpace(messages)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &list); err != nil {
		// The gap is recorded and the bytes are not. An undecodable message
		// list may still hold a served excerpt, and the one thing worse
		// than an unreadable record is an unreadable record that copied the
		// corpus into the log on the way to being unreadable.
		return w.annotate(annotationUndecodedTurn,
			fmt.Sprintf("the engine's message list did not decode as an array of objects: %v", err), at)
	}
	if len(list) < w.written {
		// The conversation came back shorter than what is persisted, so the
		// engine rewrote it: an auto-compaction replaces history with a
		// summary. Recording that and then appending the whole new list
		// keeps the reasoning as it stood and the fact that it was replaced,
		// which a reader needs in order to read the second half honestly.
		if err := w.annotate(annotationCompaction,
			fmt.Sprintf("the engine replaced the conversation: %d messages persisted, %d reported", w.written, len(list)), at); err != nil {
			return err
		}
		w.written = 0
	}
	for _, msg := range list[w.written:] {
		if err := w.message(msg, at); err != nil {
			return err
		}
	}
	w.written = len(list)
	return nil
}

// Close finishes the log. It is idempotent, so a caller that closes on both
// the success and failure paths does not have to know which one it is on.
func (w *Writer) Close() error {
	if w.file == nil {
		return nil
	}
	file := w.file
	w.file = nil
	return file.Close()
}

// message writes one message record, reducing the content of anything that
// is neither Babel's own prompt nor the model's own words.
func (w *Writer) message(msg map[string]json.RawMessage, at time.Time) error {
	role := stringField(msg, "role")
	body := any(msg)
	if role != roleUser && role != roleAssistant {
		body = w.reduce(msg)
	}
	ts := at
	if ms, ok := millisField(msg, "timestamp"); ok {
		ts = time.UnixMilli(ms)
	}
	return w.writeRecord(w.envelope(recordMessage, ts, map[string]any{"message": body}))
}

// reduce replaces a served result's content with the locators that recover
// it. Everything the record needs in order to be read as an answer to a
// call — which call, which tool, whether it failed — is kept; the body is
// not, whatever shape it arrived in.
func (w *Writer) reduce(msg map[string]json.RawMessage) map[string]any {
	locators, reason := w.redact(resultText(msg))
	if strings.TrimSpace(reason) == "" {
		reason = "the facility that served this result gave no reason; it is withheld"
	}
	document, err := json.Marshal(withheldResult{Schema: WithheldSchema, Reason: reason, Locators: locators})
	if err != nil {
		// Encoding a struct of strings and ints cannot fail, but a record
		// that silently lost its explanation would be indistinguishable
		// from one whose facility served nothing.
		document = []byte(`{"schema":"` + WithheldSchema + `","reason":"the withheld-result document could not be encoded"}`)
	}
	out := make(map[string]any, len(toolResultFields)+1)
	for _, field := range toolResultFields {
		if value, ok := msg[field]; ok {
			out[field] = value
		}
	}
	out["content"] = []map[string]string{{"type": "text", "text": string(document)}}
	return out
}

// withheldResult is what a session log holds in place of a served tool
// result: the reason the content is absent, and the locators that recover it
// from the archive.
type withheldResult struct {
	Schema   string          `json:"schema"`
	Reason   string          `json:"reason"`
	Locators []event.Locator `json:"locators,omitempty"`
}

// annotate writes one of Babel's own notes about its writing.
func (w *Writer) annotate(kind, note string, at time.Time) error {
	return w.writeRecord(w.envelope(recordCustom, at, map[string]any{
		"customType": kind,
		"data":       map[string]string{"note": note},
	}))
}

// envelope composes the record fields every record shares: the type, the
// minted id, the previous record's id, and the record's instant.
func (w *Writer) envelope(kind string, at time.Time, fields map[string]any) map[string]any {
	w.seq++
	id := fmt.Sprintf("b%08d", w.seq)
	record := map[string]any{
		"type":      kind,
		"id":        id,
		"parentId":  nil,
		"timestamp": at.UTC().Format(time.RFC3339Nano),
	}
	if w.parent != "" {
		record["parentId"] = w.parent
	}
	w.parent = id
	for key, value := range fields {
		record[key] = value
	}
	return record
}

// writeRecord appends one record as a single line in one write, so a crash
// leaves whole records behind rather than half of one.
func (w *Writer) writeRecord(record map[string]any) error {
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("babelself: encode %s record: %w", record["type"], err)
	}
	line = append(line, '\n')
	if _, err := w.file.Write(line); err != nil {
		return fmt.Errorf("babelself: write %s: %w", w.path, err)
	}
	return nil
}

// headRecord decodes the head records of a log. One shape covers the title
// and session records; Type selects which fields are meaningful.
type headRecord struct {
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Version   int      `json:"version"`
	ID        string   `json:"id"`
	RunID     string   `json:"runId"`
	Job       string   `json:"job"`
	Timestamp string   `json:"timestamp"`
	CWD       string   `json:"cwd"`
	Profile   string   `json:"profile"`
	Recipes   []string `json:"recipes"`
}

// resultText is the text a served result arrived as, which is what the
// facility's redactor decodes. Content travels either as a bare string or as
// content blocks; a shape that is neither yields the empty string, and a
// redactor answering for nothing withholds, which is the right answer for a
// result this writer could not even read.
func resultText(msg map[string]json.RawMessage) string {
	raw, ok := msg["content"]
	if !ok {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func stringField(msg map[string]json.RawMessage, field string) string {
	raw, ok := msg[field]
	if !ok {
		return ""
	}
	var out string
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out
}

// millisField reads a harness timestamp: OMP writes epoch milliseconds on a
// message. A value that is absent, not a number, or not positive leaves the
// record with the turn's own instant rather than with 1970.
func millisField(msg map[string]json.RawMessage, field string) (int64, bool) {
	raw, ok := msg[field]
	if !ok {
		return 0, false
	}
	var ms int64
	if json.Unmarshal(raw, &ms) != nil || ms <= 0 {
		return 0, false
	}
	return ms, true
}
