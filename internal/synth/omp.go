package synth

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ompProjects are the project directory names sessions are spread across. One
// carries a space and every one carries a leading dash, because that is what
// OMP's slugs actually look like and it is exactly the shape that forces an
// adapter's source-id sanitizer to run.
var ompProjects = [...]string{
	"-synthetic-project",
	"-synthetic other project",
	"-synthetic.project.dots",
}

// ompStemLayout renders the timestamp half of a session log's stem.
const ompStemLayout = "2006-01-02T15-04-05-000Z"

// generateOMP writes the OMP tree: one primary log per session under
// <root>/omp/agent/sessions/<project>/, each with its sibling artifact tree,
// plus a non-session file that discovery must ignore.
func (g *generator) generateOMP() error {
	slots := g.byHarness(HarnessOMP)
	if len(slots) == 0 {
		return nil
	}
	for _, s := range slots {
		if err := g.writeOMPSession(s); err != nil {
			return err
		}
	}
	// A stray file beside the session logs: discovery keys on the ".jsonl"
	// suffix, and a fixture that never tests that is not testing discovery.
	ignored := filepath.Join(g.corpus.OMPSessionsRoot, ompProjects[0], ".ignored-not-a-session.txt")
	return g.writeFile(ignored, []byte("synthetic non-session file; discovery must ignore it\n"))
}

func (g *generator) writeOMPSession(s *slot) error {
	project := ompProjects[s.ordinal%len(ompProjects)]
	start := sessionTime(0, s.ordinal)
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", s.ordinal)
	stem := start.Format(ompStemLayout) + "_" + id
	dir := filepath.Join(g.corpus.OMPSessionsRoot, project)
	path := filepath.Join(dir, stem+".jsonl")

	// Every reference the session closes over, resolvable ones first.
	refs := append(g.refsOf(s), s.missing...)

	// One reference is planted in a sibling subagent log instead of the
	// primary log, because a session's closure spans its whole tree and an
	// adapter that scanned only the primary log would miss it.
	var artifactRefs []string
	logRefs := refs
	if len(refs) >= 2 && s.artifacts >= 1 {
		artifactRefs = refs[1:2]
		logRefs = append(append([]string{}, refs[0]), refs[2:]...)
	}

	session := Session{
		Harness:        HarnessOMP,
		ID:             id,
		Path:           path,
		BlobRefs:       refs,
		UnresolvedRefs: append([]string(nil), s.missing...),
		Defects:        s.defects,
	}

	w, err := createLog(path)
	if err != nil {
		return err
	}

	title := fmt.Sprintf("Synthetic fixture session %02d", s.ordinal)
	if s.defects.SparseHeader {
		title = ""
	}
	// The title record is a fixed-width padded record OMP rewrites in place.
	w.open().raw(`{"type":"title","v":1,"title":`).str(title).
		raw(`,"source":`)
	if s.defects.SparseHeader {
		w.raw("null")
	} else {
		w.raw(`"auto"`)
	}
	w.raw(`,"updatedAt":`).ts(start.Add(time.Minute)).
		raw(`,"pad":`).str(strings.Repeat(" ", 40)).raw("}")
	w.end()

	w.open().raw(`{"type":"session","version":3,"id":`).str(id)
	if !s.defects.SparseHeader {
		w.raw(`,"timestamp":`).ts(start).
			raw(`,"cwd":`).str(fmt.Sprintf("/synthetic/workspace/%02d", s.ordinal)).
			raw(`,"title":`).str(title).
			raw(`,"titleSource":"auto"`)
	} else {
		w.raw(`,"cwd":"","title":""`)
	}
	w.raw("}")
	w.end()

	for i, ref := range logRefs {
		t := start.Add(time.Duration(w.records) * time.Second)
		if i%2 == 1 {
			// The nested form: a reference inside a JSON string holding more
			// JSON, which only a raw byte scan finds.
			w.open().raw(`{"type":"message","id":`).id("f", w.records).
				raw(`,"parentId":null,"timestamp":`).ts(t).
				raw(`,"message":{"role":"tool","content":[{"type":"text","text":`).
				str(`{"synthetic-artifact":"` + ref + `"}`).
				raw("}]}}")
			w.end()
			continue
		}
		w.open().raw(`{"type":"message","id":`).id("f", w.records).
			raw(`,"parentId":null,"timestamp":`).ts(t).
			raw(`,"message":{"role":"user","content":[{"type":"text","text":"synthetic fixture reference"},`).
			raw(`{"type":"image","data":`).str(ref).raw(`,"mimeType":"image/webp"}]}}`)
		w.end()
	}

	for w.size < s.target {
		t := start.Add(time.Duration(w.records) * time.Second)
		role := "user"
		if w.records%2 == 1 {
			role = "assistant"
		}
		w.open().raw(`{"type":"message","id":`).id("f", w.records).
			raw(`,"parentId":null,"timestamp":`).ts(t).
			raw(`,"message":{"role":"`).raw(role).raw(`","content":[{"type":"text","text":`).
			str(fillerSlice(bodyFillBytes)).raw("}]}}")
		w.end()
	}

	writeDefects(w, s.defects, start, ompRecordShape)
	if err := w.close(); err != nil {
		return err
	}

	session.Bytes = w.size
	session.Records = w.records
	g.account(w.size)

	if err := g.writeOMPArtifacts(&session, strings.TrimSuffix(path, ".jsonl"), s, artifactRefs); err != nil {
		return err
	}
	g.addSession(session)
	return nil
}

// ompRecordShape brackets a free-text payload in an OMP message record, so a
// deliberately damaged record is still recognizably an OMP record.
var ompRecordShape = recordShape{
	open: func(w *logWriter, t time.Time) {
		w.open().raw(`{"type":"message","id":`).id("f", w.records).
			raw(`,"parentId":null,"timestamp":`).ts(t).
			raw(`,"message":{"role":"assistant","content":[{"type":"text","text":"`)
	},
	closing: `"}]}}`,
}

// writeOMPArtifacts fills the sibling artifact tree, including a nested
// subdirectory and, when one was planted, a subagent log carrying its own blob
// reference.
func (g *generator) writeOMPArtifacts(session *Session, dir string, s *slot, refs []string) error {
	for i := range s.artifacts {
		name := ompArtifactName(i)
		path := filepath.Join(dir, filepath.FromSlash(name))
		var content []byte
		if i == 0 && len(refs) > 0 {
			content = fmt.Appendf(nil,
				"{\"type\":\"message\",\"id\":\"s%07d\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"image\",\"data\":\"%s\"}]}}\n",
				i, refs[0])
		} else {
			content = fmt.Appendf(nil, "synthetic artifact %s of session %02d\n%s\n",
				name, s.ordinal, fillerSlice(128))
		}
		if err := g.writeFile(path, content); err != nil {
			return err
		}
		session.Artifacts = append(session.Artifacts, path)
		session.ArtifactBytes += int64(len(content))
	}
	return nil
}

// ompArtifactName names artifact i, keeping the first three shapes fixed so a
// nested tree is generated as soon as a session has more than one artifact.
func ompArtifactName(i int) string {
	switch i {
	case 0:
		return "Helper.jsonl"
	case 1:
		return "nested/0.bash.log"
	case 2:
		return "nested/deep/1.txt"
	default:
		return fmt.Sprintf("%d.txt", i)
	}
}
