package publish

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atyrode/babel/internal/archive"
)

// journalSchema versions the private local resumption record. An
// unrecognized schema is treated exactly like a corrupt journal: a cold
// start, which is always safe because every publication stage is
// idempotent (SPEC.md §6.1).
const journalSchema = 1

// Publication stages, in the order Push completes them. A stage is only
// recorded after the remote read-back that makes it durable, so a journal
// never claims more progress than the archive actually holds.
const (
	stageStaged    = "staged"    // every discovered source is staged and hashed
	stageObjects   = "objects"   // bundle payloads, artifacts and blobs are durable
	stageSegments  = "segments"  // manifest segments are durable
	stageIndex     = "index"     // the generation index is durable
	stageCommitted = "committed" // the commit record is durable and read back
	stagePointer   = "pointer"   // the latest hint names the new generation
)

// journal is the private local publication record described in SPEC.md
// §6.1: it accelerates resumption of one intended generation and nothing
// more. It is never the ordering authority (decision 43) — the generation
// number always comes from the highest verified remote commit record, and
// a journal naming a different generation is discarded.
//
// Its one real short-circuit is stageCommitted: a commit record that was
// already written and read back is durable publication, so a re-run only
// has to replace the latest hint.
type journal struct {
	Schema       int               `json:"schema"`
	HostID       string            `json:"host_id"`
	Generation   uint64            `json:"generation"`
	Stage        string            `json:"stage"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Entries      map[string]string `json:"entries,omitempty"`
	CommitKey    string            `json:"commit_key,omitempty"`
	CommitDigest archive.Digest    `json:"commit_digest,omitempty"`
}

// journalPath returns the host-scoped journal file inside the state
// directory. Host IDs are validated names, so they are safe path segments.
func journalPath(stateDir, hostID string) string {
	return filepath.Join(stateDir, "publish-journal-"+hostID+".json")
}

// loadJournal reads the host's journal. An absent, unreadable, malformed,
// foreign, or future-schema journal yields nil: resumption information is
// an optimization and losing it only costs work, never correctness.
func loadJournal(stateDir, hostID string) *journal {
	raw, err := os.ReadFile(journalPath(stateDir, hostID))
	if err != nil {
		return nil
	}
	var j journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil
	}
	if j.Schema != journalSchema || j.HostID != hostID || j.Generation == 0 {
		return nil
	}
	return &j
}

// clearJournal discards resumption state once a push settles with nothing
// in flight. A journal that outlived its publication would only ever be
// discarded on the next run anyway, so failure to remove it is ignored.
func (p *Publisher) clearJournal() {
	_ = os.Remove(journalPath(p.stateDir, p.hostID))
}

// save rewrites the journal atomically: a temporary file in the state
// directory, fsynced, then renamed over the previous record, so a crash
// mid-write can never leave a half-written stage cursor behind.
func (j *journal) save(stateDir string, now time.Time) error {
	j.Schema = journalSchema
	j.UpdatedAt = now.UTC()
	raw, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("publish: marshal journal: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("publish: create state directory: %w", err)
	}
	dst := journalPath(stateDir, j.HostID)
	f, err := os.CreateTemp(stateDir, ".publish-journal-*")
	if err != nil {
		return fmt.Errorf("publish: create journal temporary: %w", err)
	}
	tmp := f.Name()
	published := false
	defer func() {
		if !published {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("publish: write journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("publish: sync journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("publish: close journal: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("publish: publish journal: %w", err)
	}
	published = true
	return nil
}
