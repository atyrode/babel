package synth

import (
	"fmt"
	"path/filepath"
	"time"
)

// claudeProjects are encoded-workspace directory names. Claude Code derives
// them by collapsing path separators and punctuation to "-", so they always
// begin with a dash and can never be decoded back into a path; one carries a
// space as well, because the encoding is lossy in both directions and an
// adapter's identity sanitizer has to survive whatever lands on disk.
var claudeProjects = [...]string{
	"-synthetic-workspace-alpha",
	"-synthetic-workspace beta",
	"-synthetic-workspace-gamma",
}

// generateClaude writes the Claude Code tree: one transcript per session under
// <root>/claude/projects/<encoded-workspace>/, plus the three session-linked
// artifact trees named after the session UUID.
func (g *generator) generateClaude() error {
	slots := g.byHarness(HarnessClaude)
	if len(slots) == 0 {
		return nil
	}
	for _, s := range slots {
		if err := g.writeClaudeSession(s); err != nil {
			return err
		}
	}
	// A non-transcript file inside a project directory: discovery keys on the
	// ".jsonl" suffix, and this is what proves it.
	notes := filepath.Join(g.corpus.ClaudeRoot, "projects", claudeProjects[0], "synthetic-notes.txt")
	return g.writeFile(notes, []byte("synthetic non-transcript file; discovery must ignore it\n"))
}

func (g *generator) writeClaudeSession(s *slot) error {
	project := claudeProjects[s.ordinal%len(claudeProjects)]
	start := sessionTime(2, s.ordinal)
	uuid := fmt.Sprintf("cccccccc-0000-4000-8000-%012d", s.ordinal)
	path := filepath.Join(g.corpus.ClaudeRoot, "projects", project, uuid+".jsonl")

	session := Session{
		Harness: HarnessClaude,
		ID:      uuid,
		Path:    path,
		Defects: s.defects,
	}
	if err := g.writeClaudeArtifacts(&session, project, uuid, s); err != nil {
		return err
	}

	w, err := createLog(path)
	if err != nil {
		return err
	}
	sparse := s.defects.SparseHeader
	cwd := fmt.Sprintf("/synthetic/workspace/%02d", s.ordinal)

	// The header-bearing fields are repeated on every ordinary record, which is
	// how Claude Code writes them; a sparse session simply never carries them.
	body := func(kind, role string, t time.Time, n int) {
		w.open().raw(`{"type":"`).raw(kind).raw(`","uuid":`).id("11111111-0000-4000-8000-", n).
			raw(`,"isSidechain":false,"userType":"external","sessionId":`).str(uuid)
		if !sparse {
			at := cwd
			if s.defects.WorkspaceMoved && n%3 == 2 {
				// A second, distinct cwd: the transcript followed a workspace
				// that moved, and the adapter reports the conflict rather than
				// picking a winner.
				at = cwd + "-moved"
			}
			w.raw(`,"cwd":`).str(at).
				raw(`,"gitBranch":"synthetic/branch","version":"9.9.999","timestamp":`).ts(t)
		}
		w.raw(`,"message":{"role":"`).raw(role).raw(`","content":[{"type":"text","text":`).
			str(fillerSlice(bodyFillBytes)).raw("}]}}")
		w.end()
	}

	if !sparse {
		w.open().raw(`{"type":"ai-title","aiTitle":`).
			str(fmt.Sprintf("Synthetic fixture transcript %02d", s.ordinal)).
			raw(`,"sessionId":`).str(uuid).raw("}")
		w.end()
	} else {
		// A transcript with no ai-title, no cwd, and no timestamp: every
		// portable field an adapter cannot observe must become an explicit
		// completeness reason rather than a synthesized value.
		w.open().raw(`{"type":"summary","summary":"synthetic fixture summary","leafUuid":`).
			id("11111111-0000-4000-8000-", 0).raw("}")
		w.end()
	}

	for w.size < s.target {
		role := "user"
		kind := "user"
		if w.records%2 == 0 {
			role, kind = "assistant", "assistant"
		}
		body(kind, role, start.Add(time.Duration(w.records)*time.Second), w.records)
	}
	if s.defects.WorkspaceMoved && w.records < 3 {
		// Guarantee the conflict even in the smallest size bucket.
		body("user", "user", start.Add(time.Duration(w.records)*time.Second), 2)
	}

	writeDefects(w, s.defects, start, claudeRecordShape(uuid, sparse))
	if err := w.close(); err != nil {
		return err
	}
	session.Bytes = w.size
	session.Records = w.records
	g.account(w.size)
	g.addSession(session)
	return nil
}

// claudeRecordShape brackets a free-text payload in a Claude Code transcript
// record. A sparse transcript omits the timestamp here too, so a defect record
// cannot quietly restore a field the session was generated without.
func claudeRecordShape(uuid string, sparse bool) recordShape {
	return recordShape{
		open: func(w *logWriter, t time.Time) {
			w.open().raw(`{"type":"assistant","uuid":`).id("11111111-0000-4000-8000-", w.records).
				raw(`,"sessionId":`).str(uuid)
			if !sparse {
				w.raw(`,"timestamp":`).ts(t)
			}
			w.raw(`,"message":{"role":"assistant","content":[{"type":"text","text":"`)
		},
		closing: `"}]}}`,
	}
}

// writeClaudeArtifacts spreads a session's artifacts across the three trees the
// format names after the session UUID, and plants a dot-prefixed file the
// adapter deliberately skips.
func (g *generator) writeClaudeArtifacts(session *Session, project, uuid string, s *slot) error {
	trees := [...]string{
		filepath.Join("projects", project, uuid),
		filepath.Join("tasks", uuid),
		filepath.Join("session-env", uuid),
	}
	for i := range s.artifacts {
		name := claudeArtifactName(i)
		path := filepath.Join(g.corpus.ClaudeRoot, trees[i%len(trees)], filepath.FromSlash(name))
		content := fmt.Appendf(nil, "{\"synthetic\":\"artifact %s of claude session %02d\",\"pad\":\"%s\"}\n",
			name, s.ordinal, fillerSlice(96))
		if err := g.writeFile(path, content); err != nil {
			return err
		}
		session.Artifacts = append(session.Artifacts, path)
		session.ArtifactBytes += int64(len(content))
	}
	if s.artifacts > 0 {
		// Transient state the adapter skips by name. It is generated so a test
		// can tell a deliberate omission from an oversight.
		hidden := filepath.Join(g.corpus.ClaudeRoot, trees[1], ".synthetic-lock")
		if err := g.writeFile(hidden, []byte("synthetic transient lock\n")); err != nil {
			return err
		}
		session.HiddenArtifacts = append(session.HiddenArtifacts, hidden)
	}
	return nil
}

// claudeArtifactName names artifact i, keeping the first two shapes fixed so a
// corpus always contains a nested artifact directory.
func claudeArtifactName(i int) string {
	switch i {
	case 0:
		return "synthetic-subagent-0000.jsonl"
	case 1:
		return "nested/synthetic-task-0001.json"
	default:
		return fmt.Sprintf("synthetic-artifact-%04d.json", i)
	}
}
