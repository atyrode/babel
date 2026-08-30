package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/synth"
)

// TestHitLocatorsRecoverTheirBytes is the property the whole analysis layer
// rests on. A hit is only evidence if its locator recovers the exact record it
// came from, so this indexes a generated corpus, searches it, and for every hit
// re-reads the file at the recorded offset and hashes the bytes independently.
// Anything less would let a plausible-looking index silently point at the wrong
// record after an offset bug.
func TestHitLocatorsRecoverTheirBytes(t *testing.T) {
	root := t.TempDir()
	corpus, err := synth.Generate(root, synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	idx, err := index.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	ctx := t.Context()
	for _, s := range corpus.Sessions {
		stream := event.Stream{Harness: s.Harness, SourceID: s.ID, Path: s.Path}
		if _, err := idx.IndexSession(ctx, stream); err != nil {
			t.Fatalf("index %s: %v", s.Path, err)
		}
	}

	// Search broadly rather than for a term: every indexed event should be
	// addressable, not only the ones a lucky query returns.
	var checked int
	for _, kind := range []event.Kind{
		event.KindUserReport, event.KindAgentClaim, event.KindToolObservation,
		event.KindRepositoryChange, event.KindVerificationEvidence, event.KindOpaque,
	} {
		hits, err := idx.Search(ctx, index.Query{Kinds: []event.Kind{kind}, Limit: 200})
		if err != nil {
			t.Fatalf("search %s: %v", kind, err)
		}
		for _, h := range hits {
			if h.Locator.Digest == "" {
				t.Fatalf("%s hit has no digest", kind)
			}
			raw, err := readRecordAt(h.Locator.Path, h.Locator.ByteOffset)
			if err != nil {
				t.Fatalf("re-read %s at %d: %v", h.Locator.Path, h.Locator.ByteOffset, err)
			}
			sum := sha256.Sum256(raw)
			if got := hex.EncodeToString(sum[:]); got != h.Locator.Digest {
				t.Errorf("%s at %s:%d digest mismatch\n stored: %s\nre-read: %s",
					kind, h.Locator.Path, h.Locator.Line, h.Locator.Digest, got)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no hits checked; the corpus or the index produced nothing")
	}
	t.Logf("verified %d hit locators against their bytes", checked)
}

// readRecordAt returns one record's bytes starting at off, excluding the
// terminator, matching how the event model computes a locator's digest.
func readRecordAt(path string, off int64) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rest := data[off:]
	for i, b := range rest {
		if b == '\n' {
			line := rest[:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, nil
		}
	}
	return rest, nil
}
