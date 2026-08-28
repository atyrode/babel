package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
)

func TestSessionListReusesUnchangedDescriptions(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(primary, []byte("synthetic session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := localSession{src: adapter.SourceSession{
		Harness:     "omp",
		SourceID:    "synthetic/session",
		PrimaryPath: primary,
	}}
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	modified := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	calls := 0
	describeSession := func(_ context.Context, got localSession) (*adapter.Description, error) {
		calls++
		return &adapter.Description{
			Source:      got.src,
			PrimarySize: int64(len("synthetic session\n")),
			Meta: adapter.CommonMeta{
				ModifiedAt: &modified,
			},
		}, nil
	}

	rows, err := a.listSessionRows(context.Background(), []localSession{session}, []string{"omp"}, root, false, describeSession, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(rows) != 1 {
		t.Fatalf("first listing calls=%d rows=%d, want 1 and 1", calls, len(rows))
	}
	calls = 0
	rows, err = a.listSessionRows(context.Background(), []localSession{session}, []string{"omp"}, root, false, describeSession, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("second unchanged listing performed %d describes, want 0", calls)
	}
	if len(rows) != 1 || rows[0].Selector != session.key() {
		t.Fatalf("second listing rows = %+v", rows)
	}
	if stderr.Len() != 0 {
		t.Fatalf("listings wrote diagnostics: %q", stderr.String())
	}
}

// TestHarnessScopedListingsKeepEachOthersRows is the regression for the
// defect that made a warm 836-session listing take a minute: a refresh that
// covered one harness used to delete every cached row belonging to the
// others, so alternating harness-filtered listings re-described the whole
// machine each time.
func TestHarnessScopedListingsKeepEachOthersRows(t *testing.T) {
	root := t.TempDir()
	sessions := map[string]localSession{}
	for _, harness := range []string{"omp", "claude"} {
		primary := filepath.Join(root, harness+".jsonl")
		if err := os.WriteFile(primary, []byte("synthetic "+harness+" session\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sessions[harness] = localSession{src: adapter.SourceSession{
			Harness:     harness,
			SourceID:    "synthetic/" + harness,
			PrimaryPath: primary,
		}}
	}
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	calls := 0
	describeSession := func(_ context.Context, got localSession) (*adapter.Description, error) {
		calls++
		return &adapter.Description{Source: got.src, PrimarySize: 1}, nil
	}
	list := func(scope []string, want []localSession) []sessionRow {
		t.Helper()
		calls = 0
		rows, err := a.listSessionRows(context.Background(), want, scope, root, false, describeSession, nil)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	if rows := list([]string{"claude"}, []localSession{sessions["claude"]}); len(rows) != 1 || calls != 1 {
		t.Fatalf("cold claude listing rows=%d describes=%d, want 1 and 1", len(rows), calls)
	}
	if rows := list([]string{"omp"}, []localSession{sessions["omp"]}); len(rows) != 1 || calls != 1 {
		t.Fatalf("cold omp listing rows=%d describes=%d, want 1 and 1", len(rows), calls)
	}
	if rows := list([]string{"claude"}, []localSession{sessions["claude"]}); len(rows) != 1 || calls != 0 {
		t.Fatalf("warm claude listing rows=%d describes=%d, want 1 and 0", len(rows), calls)
	}
	rows := list([]string{"omp", "codex", "claude"}, []localSession{sessions["claude"], sessions["omp"]})
	if len(rows) != 2 || calls != 0 {
		t.Fatalf("warm unfiltered listing rows=%d describes=%d, want 2 and 0", len(rows), calls)
	}
	if stderr.Len() != 0 {
		t.Fatalf("listings wrote diagnostics: %q", stderr.String())
	}
}
