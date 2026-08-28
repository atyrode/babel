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

	rows, err := a.listSessionRows(context.Background(), []localSession{session}, root, false, describeSession)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(rows) != 1 {
		t.Fatalf("first listing calls=%d rows=%d, want 1 and 1", calls, len(rows))
	}
	calls = 0
	rows, err = a.listSessionRows(context.Background(), []localSession{session}, root, false, describeSession)
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
