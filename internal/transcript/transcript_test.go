package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventsHarnessFixtures(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		path    string
		want    []Event
	}{
		{
			name:    "omp",
			harness: "omp",
			path: filepath.Join("..", "adapter", "omp", "testdata", "root", "agent", "sessions", "-synthetic-project",
				"2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001.jsonl"),
			want: []Event{
				{Index: 2, Role: "user", Kind: "message", Text: "synthetic fixture message one"},
				{Index: 3, Role: "assistant", Kind: "message", Text: "synthetic fixture reply one"},
			},
		},
		{
			name:    "codex",
			harness: "codex",
			path: filepath.Join("..", "adapter", "codex", "testdata", "root", "sessions", "2026", "01", "02",
				"rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-000000000001.jsonl"),
			want: []Event{
				{Index: 2, Role: "user", Kind: "message"},
				{Index: 5, Role: "assistant", Kind: "message", Text: "synthetic fixture message three"},
			},
		},
		{
			name:    "claude",
			harness: "claude",
			path:    filepath.Join("..", "adapter", "claude", "testdata", "session-rich.jsonl"),
			want: []Event{
				{Index: 2, Role: "user", Kind: "message", Text: "synthetic fixture message one"},
				{Index: 3, Role: "assistant", Kind: "message", Text: "synthetic fixture reply one"},
				{Index: 5, Role: "user", Kind: "message", Text: "synthetic fixture message two"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, got, err := Events(tt.path, tt.harness, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if total < len(tt.want) {
				t.Fatalf("total = %d, want at least %d", total, len(tt.want))
			}
			for _, want := range tt.want {
				if want.Index >= len(got) {
					t.Fatalf("missing event %d", want.Index)
				}
				event := got[want.Index]
				if event.Role != want.Role || event.Kind != want.Kind {
					t.Errorf("event %d = role %q kind %q, want role %q kind %q", want.Index, event.Role, event.Kind, want.Role, want.Kind)
				}
				if want.Text != "" && event.Text != want.Text {
					t.Errorf("event %d text = %q, want %q", want.Index, event.Text, want.Text)
				}
				if event.Time == nil {
					t.Errorf("event %d has nil time", want.Index)
				}
			}
		})
	}
}

func TestEventsRawPaginationAndTornFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	long := strings.Repeat("界", rawTextLimit+10)
	contents := "{not json}\n" + long + "\n" + `{"type":"message","timestamp":"2026-01-02T03:10:00Z","message":{"role":"user","content":[{"type":"text","text":"torn"}]}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	total, got, err := Events(path, "omp", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 2 {
		t.Fatalf("total, len = %d, %d; want 3, 2", total, len(got))
	}
	if got[0].Index != 1 || got[0].Kind != "raw" || len([]rune(got[0].Text)) != rawTextLimit {
		t.Fatalf("raw event = %#v", got[0])
	}
	if got[1].Index != 2 || got[1].Text != "torn" {
		t.Fatalf("torn event = %#v", got[1])
	}
}

func TestEventsRejectsInvalidWindow(t *testing.T) {
	if _, _, err := Events("unused", "omp", -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, _, err := Events("unused", "omp", 0, -1); err == nil {
		t.Fatal("negative limit accepted")
	}
}
