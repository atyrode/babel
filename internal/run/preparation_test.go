package run

import (
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
)

var preparedAt = time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)

func testDigest(seed string) digest.Digest { return digest.Bytes([]byte(seed)) }

// testSelection returns a three-harness selection whose entries differ in
// every field that participates in the preparation ID.
func testSelection() []Selected {
	return []Selected{
		{
			Host:          "host-a",
			Harness:       event.HarnessOMP,
			SourceID:      "session-0001",
			Snapshot:      "0a1b2c3d",
			CaptureDigest: testDigest("omp-capture"),
			SourceDigest:  testDigest("omp-normalized"),
			Adapter:       AdapterRef{Schema: 3, Version: "omp-1.2.0"},
		},
		{
			Host:          "host-a",
			Harness:       event.HarnessCodex,
			SourceID:      "rollout-0002",
			CaptureDigest: testDigest("codex-capture"),
			SourceDigest:  testDigest("codex-normalized"),
			Adapter: AdapterRef{Schema: 1, Version: "codex-0.4.1", Completeness: []adapter.CompletenessReason{
				{Field: "title", Reason: "format exposes none"},
			}},
		},
		{
			Host:          "host-b",
			Harness:       event.HarnessClaude,
			SourceID:      "project/session-0003",
			Snapshot:      "ff00aa11",
			CaptureDigest: testDigest("claude-capture"),
			SourceDigest:  testDigest("claude-normalized"),
			Adapter: AdapterRef{Schema: 2, Version: "claude-0.9.0", Completeness: []adapter.CompletenessReason{
				{Field: "workspace", Reason: "no project marker"},
				{Field: "lifecycle", Reason: "not recorded on disk"},
			}},
		},
	}
}

func mustPreparation(t *testing.T, at time.Time, selection []Selected) Preparation {
	t.Helper()
	p, err := NewPreparation(at, selection)
	if err != nil {
		t.Fatalf("NewPreparation: %v", err)
	}
	return p
}

func TestPreparationIDIsStableUnderSelectionReordering(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())

	reversed := testSelection()
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	if got := mustPreparation(t, preparedAt, reversed); got.ID != base.ID {
		t.Errorf("reversing the selection changed the id:\n got %s\nwant %s", got.ID, base.ID)
	}

	// The completeness report is adapter output whose order is incidental, so
	// it must not reach the identity either.
	shuffledReasons := testSelection()
	r := shuffledReasons[2].Adapter.Completeness
	r[0], r[1] = r[1], r[0]
	if got := mustPreparation(t, preparedAt, shuffledReasons); got.ID != base.ID {
		t.Errorf("reordering completeness reasons changed the id:\n got %s\nwant %s", got.ID, base.ID)
	}

	if !base.ID.Valid() {
		t.Errorf("derived id %q is not well-formed", base.ID)
	}
	if err := base.Verify(); err != nil {
		t.Errorf("Verify on a freshly derived record: %v", err)
	}
}

// Every field of the record is hashed, so every field must be able to change
// the ID. A field that cannot is a field a later exploration could differ in
// while claiming the same scope.
func TestPreparationIDChangesWithEveryContentChange(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())

	cases := []struct {
		name   string
		at     time.Time
		mutate func(s []Selected) []Selected
	}{
		{name: "prepared at", at: preparedAt.Add(time.Nanosecond)},
		{name: "host", mutate: func(s []Selected) []Selected { s[0].Host = "host-c"; return s }},
		{name: "harness", mutate: func(s []Selected) []Selected {
			s[0].Harness = event.HarnessCodex
			return s
		}},
		{name: "source id", mutate: func(s []Selected) []Selected { s[0].SourceID = "session-9999"; return s }},
		{name: "snapshot", mutate: func(s []Selected) []Selected { s[0].Snapshot = "deadbeef"; return s }},
		{name: "snapshot removed", mutate: func(s []Selected) []Selected { s[0].Snapshot = ""; return s }},
		{name: "capture digest", mutate: func(s []Selected) []Selected {
			s[0].CaptureDigest = testDigest("other-capture")
			return s
		}},
		{name: "source digest", mutate: func(s []Selected) []Selected {
			s[0].SourceDigest = testDigest("other-normalized")
			return s
		}},
		{name: "adapter schema", mutate: func(s []Selected) []Selected { s[0].Adapter.Schema = 4; return s }},
		{name: "adapter version", mutate: func(s []Selected) []Selected {
			s[0].Adapter.Version = "omp-1.2.1"
			return s
		}},
		{name: "completeness added", mutate: func(s []Selected) []Selected {
			s[0].Adapter.Completeness = []adapter.CompletenessReason{{Field: "title", Reason: "torn final line"}}
			return s
		}},
		{name: "completeness field", mutate: func(s []Selected) []Selected {
			s[1].Adapter.Completeness[0].Field = "workspace"
			return s
		}},
		{name: "completeness reason", mutate: func(s []Selected) []Selected {
			s[1].Adapter.Completeness[0].Reason = "unreadable"
			return s
		}},
		{name: "completeness removed", mutate: func(s []Selected) []Selected {
			s[1].Adapter.Completeness = nil
			return s
		}},
		{name: "entry removed", mutate: func(s []Selected) []Selected { return s[:2] }},
		{name: "entry added", mutate: func(s []Selected) []Selected {
			extra := s[0]
			extra.SourceID = "session-0004"
			return append(s, extra)
		}},
	}

	seen := map[PreparationID]string{base.ID: "base"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := preparedAt
			if !tc.at.IsZero() {
				at = tc.at
			}
			selection := testSelection()
			if tc.mutate != nil {
				selection = tc.mutate(selection)
			}
			got := mustPreparation(t, at, selection)
			if got.ID == base.ID {
				t.Fatalf("changing %s left the id unchanged: %s", tc.name, got.ID)
			}
			if prior, dup := seen[got.ID]; dup {
				t.Fatalf("changing %s produced the same id as %s", tc.name, prior)
			}
			seen[got.ID] = tc.name
		})
	}
}

// The domain string is what keeps a preparation ID from colliding with some
// other SHA-256 over the same bytes, so it has to actually be in the hash.
func TestPreparationIDIsDomainSeparated(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())
	if !strings.HasPrefix(string(base.ID), "prep-") {
		t.Fatalf("id %q lacks its kind prefix", base.ID)
	}
	undomained := base
	undomained.Schema = PreparationSchema + 1
	if undomained.derive() == base.ID {
		t.Error("the schema version does not participate in the derivation")
	}
}

func TestPreparationCanonicalRoundTrip(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())
	encoded, err := base.MarshalCanonical()
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}
	decoded, err := UnmarshalPreparation(encoded)
	if err != nil {
		t.Fatalf("UnmarshalPreparation: %v", err)
	}
	if decoded.ID != base.ID || !decoded.PreparedAt.Equal(base.PreparedAt) {
		t.Fatalf("decoded identity %s at %s, want %s at %s",
			decoded.ID, decoded.PreparedAt, base.ID, base.PreparedAt)
	}
	again, err := decoded.MarshalCanonical()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(again) != string(encoded) {
		t.Errorf("canonical form is not stable:\n got %s\nwant %s", again, encoded)
	}
}

func TestPreparationVerifyRejectsAlteredContent(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())
	altered := base
	altered.Selection = append([]Selected(nil), base.Selection...)
	altered.Selection[0].SourceDigest = testDigest("swapped")
	if err := altered.Verify(); err == nil {
		t.Fatal("Verify accepted content that no longer derives its id")
	}
	if _, err := altered.MarshalCanonical(); err == nil {
		t.Fatal("MarshalCanonical wrote a record whose id does not match its content")
	}
}

func TestUnmarshalPreparationRejectsUnknownAndTampered(t *testing.T) {
	base := mustPreparation(t, preparedAt, testSelection())
	encoded, err := base.MarshalCanonical()
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}

	tampered := strings.Replace(string(encoded), `"host-a"`, `"host-z"`, 1)
	if _, err := UnmarshalPreparation([]byte(tampered)); err == nil {
		t.Error("decoded a record whose content was edited outside Babel")
	}

	// An unknown field cannot participate in the derivation, so accepting one
	// would mean storing content the ID does not cover.
	extended := strings.Replace(string(encoded), `{"schema":`, `{"extra":1,"schema":`, 1)
	if _, err := UnmarshalPreparation([]byte(extended)); err == nil {
		t.Error("decoded a record carrying a field outside the schema")
	}
}

func TestNewPreparationRejectsInvalidSelections(t *testing.T) {
	valid := testSelection()[0]
	duplicate := testSelection()[0]

	cases := []struct {
		name      string
		at        time.Time
		selection []Selected
	}{
		{name: "no selection", at: preparedAt},
		{name: "no time", selection: []Selected{valid}},
		{name: "duplicate session", at: preparedAt, selection: []Selected{valid, duplicate}},
		{name: "unknown harness", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.Harness = "gemini"
		})}},
		{name: "no host", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) { s.Host = "" })}},
		{name: "bad source id", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.SourceID = "../escape"
		})}},
		{name: "bad snapshot", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.Snapshot = "not hex"
		})}},
		{name: "bad capture digest", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.CaptureDigest = "sha256:short"
		})}},
		{name: "bad source digest", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.SourceDigest = ""
		})}},
		{name: "no adapter schema", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.Adapter.Schema = 0
		})}},
		{name: "no adapter version", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.Adapter.Version = ""
		})}},
		{name: "half a completeness reason", at: preparedAt, selection: []Selected{mutate(valid, func(s *Selected) {
			s.Adapter.Completeness = []adapter.CompletenessReason{{Field: "title"}}
		})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPreparation(tc.at, tc.selection); err == nil {
				t.Fatal("accepted an invalid preparation")
			}
		})
	}
}

// NewPreparation copies its input, so a caller that keeps writing to the slice
// it handed over cannot change what an already-derived ID means.
func TestNewPreparationCopiesItsSelection(t *testing.T) {
	selection := testSelection()
	p := mustPreparation(t, preparedAt, selection)
	for i := range selection {
		selection[i].Host = "mutated"
	}
	if err := p.Verify(); err != nil {
		t.Fatalf("caller mutation reached the record: %v", err)
	}
}

func mutate(s Selected, f func(*Selected)) Selected {
	f(&s)
	return s
}
