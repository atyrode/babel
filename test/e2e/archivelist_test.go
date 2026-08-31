package e2e_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// archiveListRow decodes a listing row with the continuation grade kept
// nullable. The shared sessionRow in this package models it as a bool, which
// cannot distinguish "the transcript says this session is not
// continuation-ready" from "no transcript was read at all" — and that
// distinction is exactly what a cross-host listing has to express.
type archiveListRow struct {
	Harness  string  `json:"harness"`
	SourceID string  `json:"source_id"`
	Selector string  `json:"selector"`
	Size     int64   `json:"size"`
	Modified *string `json:"modified"`
	Title    *string `json:"title"`
	// TitleProvenance distinguishes a harness-recorded title from one Babel
	// derived, so a reader can tell a fact from an inference. This suite
	// decodes with DisallowUnknownFields, which is why an unmirrored field
	// fails here rather than reaching an operator as a dropped value.
	TitleProv  *string `json:"title_provenance"`
	Workspace  *string `json:"workspace"`
	Continuous *bool   `json:"continuation_grade"`
	// The usage summary, absent for a cross-host listing for the same reason
	// the title is: nothing read the transcript.
	CostUSD     *float64 `json:"cost_usd"`
	TotalTokens *int64   `json:"total_tokens"`
	Turns       *int64   `json:"turns"`
	ToolErrors  *int64   `json:"tool_errors"`
}

type archiveListResult struct {
	Sessions []archiveListRow `json:"sessions"`
}

// selectorsOf is the set of selectors a listing reported, sorted so two
// listings can be compared as sets.
func selectorsOf(rows []archiveListRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Selector)
	}
	slices.Sort(out)
	return out
}

// requireArchiveOnlyFields holds one cross-host row to the rule that decides
// whether this feature is honest: the identity and the primary log's recorded
// size come from the snapshot's file listing, and every field that would
// require downloading and parsing the transcript is absent rather than
// zero-valued.
func requireArchiveOnlyFields(t *testing.T, row archiveListRow) {
	t.Helper()
	if row.Selector != row.Harness+"/"+row.SourceID {
		t.Fatalf("archive row selector %q does not compose from harness and source id: %+v", row.Selector, row)
	}
	if row.Size <= 0 {
		t.Fatalf("archive row %s reports no primary size although the snapshot listing records one: %+v", row.Selector, row)
	}
	if row.Modified != nil {
		t.Fatalf("archive row %s reports a modification time, which no file listing observes: %q", row.Selector, *row.Modified)
	}
	if row.Title != nil {
		t.Fatalf("archive row %s reports a title, which only the transcript carries: %q", row.Selector, *row.Title)
	}
	if row.Workspace != nil {
		t.Fatalf("archive row %s reports a workspace, which only the transcript carries: %q", row.Selector, *row.Workspace)
	}
	if row.Continuous != nil {
		t.Fatalf("archive row %s graded continuation as %v without reading its closure", row.Selector, *row.Continuous)
	}
	if row.CostUSD != nil || row.TotalTokens != nil || row.Turns != nil || row.ToolErrors != nil {
		t.Fatalf("archive row %s reports usage, which only the transcript's own records carry: %+v",
			row.Selector, row)
	}
}

// Cross-host listing is the discovery half of cross-host fetch. Fetching
// `--host HOST SELECTOR` already works, but only for a selector the operator
// already knows — and on a second machine nothing local can supply one, because
// the sessions were never here.
//
// This is the acceptance criterion for that half: the selectors a cross-host
// listing reports are exactly the selectors a local listing reports for the same
// sessions, they keep being reported once every local trace is gone, and fetch
// accepts them.
func TestArchiveListNamesTheSessionsAnotherHostArchived(t *testing.T) {
	e := newEnv(t)
	src := e.writeSources(t)

	local := okJSON[archiveListResult](t, e, "sessions", "list", "--json")
	localSelectors := selectorsOf(local.Sessions)
	if len(localSelectors) == 0 {
		t.Fatal("the local listing found no sessions; the comparison below would be vacuous")
	}

	e.bootstrapRepo(t)
	push := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if push.Incomplete {
		t.Fatalf("push reported an incomplete backup: %+v", push)
	}

	archived := okJSON[archiveListResult](t, e,
		e.with("sessions", "list", "--host", hostID, "--json")...)
	if got := selectorsOf(archived.Sessions); !slices.Equal(got, localSelectors) {
		t.Fatalf("archive listing selectors = %v, want the local listing's %v", got, localSelectors)
	}
	for _, row := range archived.Sessions {
		requireArchiveOnlyFields(t, row)
	}

	// Naming the snapshot explicitly must select the same one the default did.
	pinned := okJSON[archiveListResult](t, e,
		e.with("sessions", "list", "--host", hostID, "--snapshot", push.SnapshotID, "--json")...)
	if got := selectorsOf(pinned.Sessions); !slices.Equal(got, localSelectors) {
		t.Fatalf("--snapshot %s listed %v, want %v", push.SnapshotID, got, localSelectors)
	}

	// Become the second machine: no source trees and no local catalog, so
	// nothing on this disk can name these sessions any more.
	for _, dir := range []string{e.ompSessions, e.ompBlobs, e.codexHome, e.claudeHome, e.dataHome} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if stranded := okJSON[archiveListResult](t, e, "sessions", "list", "--json"); len(stranded.Sessions) != 0 {
		t.Fatalf("a local listing still named %v after the sources were deleted", selectorsOf(stranded.Sessions))
	}

	recovered := okJSON[archiveListResult](t, e,
		e.with("sessions", "list", "--host", hostID, "--json")...)
	if got := selectorsOf(recovered.Sessions); !slices.Equal(got, localSelectors) {
		t.Fatalf("archive listing after deleting the sources = %v, want %v", got, localSelectors)
	}
	for _, row := range recovered.Sessions {
		requireArchiveOnlyFields(t, row)
	}

	// One selector vocabulary: what the listing hands out is what fetch takes.
	// Discovery is only worth having if it feeds recovery.
	fetched := okJSON[fetchResult](t, e,
		e.with("sessions", "fetch", src.richSelector, "--host", hostID, "--json")...)
	if fetched.Selector != src.richSelector {
		t.Fatalf("fetching a listed selector resolved %q, want %q", fetched.Selector, src.richSelector)
	}
}

// The archive view has to be usable by a human and narrowable like the local
// one: absence rendered as absence, --harness still a filter, and an
// unpublished host named as the problem rather than reported as an empty
// archive. One push serves all three.
func TestArchiveListRendersAndNarrowsTheArchiveView(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)
	e.bootstrapRepo(t)
	okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)

	stdout, _ := e.ok(t, e.with("sessions", "list", "--host", hostID)...)
	if !strings.Contains(stdout, "HARNESS") {
		t.Fatalf("cross-host listing wrote no table header:\n%s", stdout)
	}
	var rendered int
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, "omp") && !strings.HasPrefix(line, "codex") && !strings.HasPrefix(line, "claude") {
			continue
		}
		// HARNESS, SOURCE ID, SIZE, MODIFIED, TITLE, WORKSPACE, GRADE, ORIGIN.
		// No fixture identity holds a space, so the columns split on
		// whitespace.
		fields := strings.Fields(line)
		if len(fields) != 8 {
			t.Fatalf("table row %q split into %d columns, want 8", line, len(fields))
		}
		// Every best-effort column is absent for another host's sessions, and
		// absence must render as absence rather than as a guess. GRADE is one
		// of them: continuation grade is resolved from local files, which this
		// machine does not have for a session it never held. So is ORIGIN: a
		// title's provenance cannot be known for a title that was never read.
		for i, column := range []string{"MODIFIED", "TITLE", "WORKSPACE", "GRADE", "ORIGIN"} {
			if fields[3+i] != "-" {
				t.Fatalf("row %q renders %s as %q, want %q", fields[1], column, fields[3+i], "-")
			}
		}
		rendered++
	}
	if rendered == 0 {
		t.Fatalf("cross-host listing rendered no session rows:\n%s", stdout)
	}

	all := okJSON[archiveListResult](t, e,
		e.with("sessions", "list", "--host", hostID, "--json")...)
	var wantCodex []string
	for _, row := range all.Sessions {
		if row.Harness == "codex" {
			wantCodex = append(wantCodex, row.Selector)
		}
	}
	if len(wantCodex) == 0 || len(wantCodex) == len(all.Sessions) {
		t.Fatalf("the archive holds %d sessions of which %d are codex; the filter check would be vacuous",
			len(all.Sessions), len(wantCodex))
	}
	slices.Sort(wantCodex)

	only := okJSON[archiveListResult](t, e,
		e.with("sessions", "list", "--host", hostID, "--harness", "codex", "--json")...)
	if got := selectorsOf(only.Sessions); !slices.Equal(got, wantCodex) {
		t.Fatalf("--harness codex over the archive listed %v, want %v", got, wantCodex)
	}

	_, stderr, code := e.run(t, e.with("sessions", "list", "--host", "no-such-host", "--json")...)
	if code == 0 {
		t.Fatal("listing a host that published nothing succeeded")
	}
	if !strings.Contains(stderr, `no snapshots for host "no-such-host"`) {
		t.Fatalf("the unknown host was not named as the problem: %s", stderr)
	}
	if !strings.Contains(stderr, hostID) {
		t.Fatalf("the error did not name the hosts the repository does hold: %s", stderr)
	}
}

// The two listing sources must never be mixed by precedence: a local-scan flag
// paired with --host, or an archive flag without it, is an invocation whose
// intent is unknowable, so each is refused by name. No push is needed — every
// case here is decided before the repository is read, including the two that
// establish that --host requires a repository selection at all.
func TestArchiveListRejectsMixedListingSources(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "snapshot without host",
			args: []string{"sessions", "list", "--snapshot", "deadbeef"},
			want: "--snapshot names a snapshot in the archive",
		},
		{
			name: "roots with host",
			args: e.with("sessions", "list", "--host", hostID, "--roots", filepath.Join(e.root, "elsewhere")),
			want: "--roots scans local source trees",
		},
		{
			name: "no-cache with host",
			args: e.with("sessions", "list", "--host", hostID, "--no-cache"),
			want: "--no-cache controls the local description cache",
		},
		{
			name: "host without a repository",
			args: []string{"sessions", "list", "--host", hostID},
			want: "no restic repository selected",
		},
		{
			name: "host without a password file",
			args: []string{"sessions", "list", "--host", hostID, "--repo", e.repoDir},
			want: "no repository password file selected",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := e.run(t, tc.args...)
			if code == 0 {
				t.Fatalf("babel %s succeeded\nstdout:\n%s", strings.Join(tc.args, " "), stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr does not name the problem %q:\n%s", tc.want, stderr)
			}
			if stdout != "" {
				t.Fatalf("a rejected invocation wrote to stdout:\n%s", stdout)
			}
		})
	}
}
