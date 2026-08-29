package synth

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	// codexAttachmentPrefix is the synthetic absolute path attachments are
	// referenced by inside message text. Codex records the real absolute path,
	// but a generated corpus cannot: a path containing the temporary directory
	// it was generated into would make two generations of one seed differ. The
	// adapter recovers only the "<id>" component, so a fixed synthetic prefix
	// is resolved exactly as a real one is.
	codexAttachmentPrefix = "/synthetic/home/.codex/attachments/"

	// codexRolloutLayout renders the timestamp inside a rollout file name.
	codexRolloutLayout = "2006-01-02T15-04-05"

	// codexUnreferencedID names an attachment directory no record mentions,
	// because a real attachments tree outlives the messages that referenced it
	// and a closure must not claim what nothing points at.
	codexUnreferencedID = "dddddddd-0000-4000-8000-000000000999"
)

// generateCodex writes the Codex tree: date-partitioned rollout logs under
// <root>/codex/sessions/, the two host-level state files, and the attachments
// referenced from message text.
func (g *generator) generateCodex() error {
	slots := g.byHarness(HarnessCodex)
	if len(slots) == 0 {
		return nil
	}
	for _, s := range slots {
		if err := g.writeCodexSession(s); err != nil {
			return err
		}
	}
	if err := g.writeCodexHostState(slots); err != nil {
		return err
	}
	g.corpus.CodexUnreferencedAttachment = filepath.Join(g.corpus.CodexRoot, "attachments", codexUnreferencedID)
	return g.writeFile(
		filepath.Join(g.corpus.CodexUnreferencedAttachment, "synthetic-unreferenced-0000.png"),
		[]byte("synthetic unreferenced attachment payload\n"),
	)
}

func (g *generator) writeCodexSession(s *slot) error {
	start := sessionTime(1, s.ordinal)
	thread := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", s.ordinal)
	sessionID := fmt.Sprintf("aaaaaaaa-0000-4000-8000-1%011d", s.ordinal)
	path := filepath.Join(g.corpus.CodexRoot, "sessions",
		start.Format("2006"), start.Format("01"), start.Format("02"),
		"rollout-"+start.Format(codexRolloutLayout)+"-"+thread+".jsonl")

	session := Session{
		Harness:        HarnessCodex,
		ID:             thread,
		Path:           path,
		UnresolvedRefs: append([]string(nil), s.missing...),
		Defects:        s.defects,
	}

	// An attachment directory is only referenced when it has files: a
	// reference to an empty directory is an unresolved reference, and this
	// corpus places those deliberately rather than by accident.
	attachmentID := fmt.Sprintf("bbbbbbbb-0000-4000-8000-%012d", s.ordinal)
	refIDs := make([]string, 0, 1+len(s.missing))
	if s.artifacts > 0 {
		refIDs = append(refIDs, attachmentID)
		session.BlobRefs = append(session.BlobRefs, "attachments/"+attachmentID)
	}
	for _, ref := range s.missing {
		refIDs = append(refIDs, strings.TrimPrefix(ref, "attachments/"))
		session.BlobRefs = append(session.BlobRefs, ref)
	}

	if err := g.writeCodexAttachments(&session, attachmentID, s); err != nil {
		return err
	}

	w, err := createLog(path)
	if err != nil {
		return err
	}
	sparse := s.defects.SparseHeader
	stamp := func(t time.Time) {
		if !sparse {
			w.raw(`"timestamp":`).ts(t).raw(",")
		}
	}

	w.open().raw("{")
	stamp(start)
	w.raw(`"type":"session_meta","payload":{"session_id":`).str(sessionID).
		raw(`,"id":`).str(thread).
		raw(`,"parent_thread_id":`).str(fmt.Sprintf("aaaaaaaa-0000-4000-8000-9%011d", s.ordinal)).
		raw(`,"originator":"synthetic-fixture-harness","cli_version":"0.0.0-synthetic"`).
		raw(`,"thread_source":"fixture","model_provider":"synthetic-provider"`).
		raw(`,"history_mode":"legacy","multi_agent_version":"disabled"`)
	if !sparse {
		w.raw(`,"timestamp":`).ts(start).
			raw(`,"cwd":`).str(fmt.Sprintf("/synthetic/workspace/%02d", s.ordinal))
	}
	w.raw("}}")
	w.end()

	w.open().raw("{")
	stamp(start.Add(time.Second))
	w.raw(`"type":"turn_context","payload":{"approval_policy":"on-request","model":"synthetic-model-a","summary":"none","turn_id":"synthetic-turn-1"`)
	if !sparse {
		w.raw(`,"cwd":`).str(fmt.Sprintf("/synthetic/workspace/%02d", s.ordinal)).
			raw(`,"workspace_roots":["/synthetic/workspace/`).raw(fmt.Sprintf("%02d", s.ordinal)).raw(`","/synthetic/other-root"]`)
	}
	w.raw("}}")
	w.end()

	if len(refIDs) > 0 {
		w.open().raw("{")
		stamp(start.Add(2 * time.Second))
		w.raw(`"type":"response_item","payload":{"id":"synthetic-item-1","type":"message","role":"user","content":[{"type":"input_text","text":`)
		var text strings.Builder
		text.WriteString("synthetic fixture message\n\n# Files mentioned by the user:\n")
		for i, id := range refIDs {
			name := codexAttachmentFile(i)
			fmt.Fprintf(&text, "\n## %s: %s%s/%s\n", name, codexAttachmentPrefix, id, name)
		}
		w.str(text.String()).raw("}]}}")
		w.end()
	}

	for w.size < s.target {
		w.open().raw("{")
		stamp(start.Add(time.Duration(w.records) * time.Second))
		w.raw(`"type":"event_msg","payload":{"type":"agent_message","message":`).
			str(fillerSlice(bodyFillBytes)).raw("}}")
		w.end()
	}

	writeDefects(w, s.defects, start, codexRecordShape(sparse))
	if err := w.close(); err != nil {
		return err
	}
	session.Bytes = w.size
	session.Records = w.records
	g.account(w.size)
	g.addSession(session)
	return nil
}

// codexRecordShape brackets a free-text payload in a Codex event record. A
// sparse session omits the timestamp here too: a defect record that smuggled a
// parsable timestamp back into a session generated without one would quietly
// undo the completeness reason it is supposed to produce.
func codexRecordShape(sparse bool) recordShape {
	return recordShape{
		open: func(w *logWriter, t time.Time) {
			w.open().raw("{")
			if !sparse {
				w.raw(`"timestamp":`).ts(t).raw(",")
			}
			w.raw(`"type":"event_msg","payload":{"type":"agent_message","message":"`)
		},
		closing: `"}}`,
	}
}

// writeCodexAttachments fills the referenced attachment directory, including a
// name with a space and a nested subdirectory, both of which the adapter's
// recursive walk must handle.
func (g *generator) writeCodexAttachments(session *Session, id string, s *slot) error {
	dir := filepath.Join(g.corpus.CodexRoot, "attachments", id)
	for i := range s.artifacts {
		name := codexAttachmentFile(i)
		path := filepath.Join(dir, filepath.FromSlash(name))
		content := fmt.Appendf(nil, "synthetic attachment %s of codex session %02d\n%s\n",
			name, s.ordinal, fillerSlice(96))
		if err := g.writeFile(path, content); err != nil {
			return err
		}
		session.Artifacts = append(session.Artifacts, path)
		session.ArtifactBytes += int64(len(content))
	}
	return nil
}

// codexAttachmentFile names attachment file i. The first two shapes are fixed
// so a corpus always contains a name with a space and a nested one.
func codexAttachmentFile(i int) string {
	switch i {
	case 0:
		return "synthetic capture 0000.txt"
	case 1:
		return "nested/synthetic-attachment-0001.png"
	default:
		return fmt.Sprintf("synthetic-attachment-%04d.bin", i)
	}
}

// writeCodexHostState writes the two files Codex keeps per host rather than per
// session. Its adapter discovers history.jsonl as one additional session with
// session_index.jsonl as that session's only artifact.
func (g *generator) writeCodexHostState(slots []*slot) error {
	var history, index []byte
	for i, s := range slots {
		t := sessionTime(1, s.ordinal)
		history = fmt.Appendf(history,
			"{\"session_id\":\"aaaaaaaa-0000-4000-8000-1%011d\",\"ts\":%d,\"text\":\"synthetic fixture prompt %02d\"}\n",
			s.ordinal, t.Unix(), i)
		index = fmt.Appendf(index,
			"{\"id\":\"aaaaaaaa-0000-4000-8000-%012d\",\"thread_name\":\"synthetic fixture thread %02d\",\"updated_at\":\"%s\"}\n",
			s.ordinal, i, t.Add(time.Hour).Format(timeLayout))
	}
	if err := g.writeFile(g.corpus.CodexHistoryPath, history); err != nil {
		return err
	}
	return g.writeFile(g.corpus.CodexIndexPath, index)
}
