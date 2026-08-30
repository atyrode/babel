package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// dashProject is a project directory named the way Claude Code and OMP name
// theirs: a workspace path with its separators replaced by dashes, which
// leaves a leading dash on every source id derived from it. That shape is
// what made a copy-pasted bare source id unparseable.
const dashProject = "-synthetic-workspace-dashed"

// dashSessionStem names the session inside dashProject.
const dashSessionStem = "2026-01-05T12-13-14-151Z_00000000-0000-4000-8000-000000000004"

// writeDashSession materializes one session whose source id begins with "-"
// and returns that source id: exactly what an operator copies out of a
// listing and pastes back into inspect or fetch.
func (f *fixture) writeDashSession() string {
	f.t.Helper()
	f.writeSession(sessionSpec{
		project:   dashProject,
		stem:      dashSessionStem,
		id:        "00000000-0000-4000-8000-000000000004",
		title:     "Synthetic fixture session four",
		workspace: "/synthetic/workspace/dashed",
	})
	return dashProject + "/" + dashSessionStem
}

// A selector is positional, so a leading dash belongs to it rather than
// starting a flag. Every Claude Code and OMP source id begins with one,
// because both encode a workspace path, so the natural copy-paste of a bare
// source id has to resolve without the operator having discovered the "--"
// terminator or the harness-prefixed form.
func TestDashLeadingSelectorIsAnOperandNotAFlag(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	sourceID := f.writeDashSession()

	// Discovery reports exactly the id the operator will paste back, so the
	// selector under test is the one the tool itself hands out.
	stdout, _ := f.ok("sessions", "list", "--json")
	listed := decode[sessionsResult](t, stdout).Sessions
	if !slices.ContainsFunc(listed, func(r sessionRow) bool { return r.SourceID == sourceID }) {
		t.Fatalf("discovery did not report the dash-leading source id %q: %+v", sourceID, listed)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"bare source id", []string{"sessions", "inspect", sourceID, "--json"}},
		{"boolean flag before the selector", []string{"sessions", "inspect", "--json", sourceID}},
		{"the terminator still works", []string{"sessions", "inspect", "--json", "--", sourceID}},
		{"the harness-prefixed form still works", []string{"sessions", "inspect", "omp/" + sourceID, "--json"}},
		{"value flag after the selector", []string{"sessions", "inspect", sourceID, "--roots", f.sessionsDir, "--json"}},
		{"value flag before the selector", []string{"sessions", "inspect", "--roots", f.sessionsDir, sourceID, "--json"}},
	} {
		stdout, stderr, code := f.run(tc.args...)
		if code != exitOK {
			t.Fatalf("%s: babel %s exited %d\nstdout:\n%s\nstderr:\n%s",
				tc.name, strings.Join(tc.args, " "), code, stdout, stderr)
		}
		if got := decode[inspectResult](t, stdout).SourceID; got != sourceID {
			t.Fatalf("%s: inspect resolved source id %q, want %q", tc.name, got, sourceID)
		}
	}
}

// Deferring an undefined dash-leading token to the operands must not cost the
// flag validation that makes a mistyped flag a rejected invocation instead of
// a selector nobody meant.
func TestMisspelledFlagsAreStillRejected(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	sourceID := f.writeDashSession()

	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		// A long spelling is unambiguous: no workspace path begins with two
		// separators, so "--jsn" can only have been a flag.
		{"long misspelling before the selector", []string{"sessions", "inspect", "--jsn", sourceID}, []string{"flag provided but not defined"}},
		{"long misspelling after the selector", []string{"sessions", "inspect", sourceID, "--jsn"}, []string{"flag provided but not defined"}},
		// A short spelling has the shape of a selector, so the invocation is
		// rejected on arity — and names both readings, because a Claude or
		// OMP selector and a mistyped short flag are indistinguishable here.
		{"short misspelling before the selector", []string{"sessions", "inspect", "-jsn", sourceID},
			[]string{"takes exactly one SELECTOR, got 2", `"-jsn"`, "never as a flag", "run with -h"}},
		{"short misspelling after the selector", []string{"sessions", "inspect", sourceID, "-jsn"},
			[]string{"takes exactly one SELECTOR, got 2", `"-jsn"`, "never as a flag", "run with -h"}},
		// A command with no operand to spend cannot want one at all, so
		// there the flag reading is the only one left.
		{"short misspelling on a command taking no operands", []string{"sessions", "list", "-jsn"}, []string{"flag provided but not defined: -jsn"}},
		{"long misspelling on a command taking no operands", []string{"sessions", "list", "--nope"}, []string{"flag provided but not defined"}},
		// Malformed flag syntax is still the flag package's own finding.
		{"malformed flag syntax", []string{"sessions", "inspect", "---json", sourceID}, []string{"bad flag syntax"}},
		// A value flag still consumes the next token, dash or not, so this
		// invocation is missing its selector rather than holding two.
		{"a dash-leading token is still a flag value", []string{"sessions", "inspect", "--roots", sourceID}, []string{"requires a SELECTOR"}},
		{"no selector at all", []string{"sessions", "inspect"}, []string{"requires a SELECTOR"}},
		// Two dash-leading operands could be two selectors or a typo beside
		// one, so the same message serves both.
		{"two dash-leading operands", []string{"sessions", "inspect", "-one", "-two"},
			[]string{"takes exactly one SELECTOR, got 2", `"-one", "-two"`, "never as a flag"}},
	} {
		_, stderr := f.mustExit(exitUsage, tc.args...)
		for _, want := range tc.want {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%s: babel %s reported %q, want it to contain %q",
					tc.name, strings.Join(tc.args, " "), stderr, want)
			}
		}
	}

	// Help is still served on stdout at exit 0: -h and -help are the flag
	// package's own names and were never candidates for an operand.
	for _, args := range [][]string{
		{"sessions", "inspect", "-h"},
		{"sessions", "inspect", "--help"},
	} {
		stdout, _ := f.ok(args...)
		if !strings.Contains(stdout, "Usage: babel sessions inspect") {
			t.Fatalf("babel %s served %q, want the inspect usage", strings.Join(args, " "), stdout)
		}
	}
}

// parse mirrors the flag package's own tokenization, so what it hands back to
// the flag package parses exactly as it would have: values are still claimed
// by the flag before them, the terminator still terminates, and a rejection
// still carries the flag package's wording.
func TestParseSeparatesDashLeadingOperandsFromFlags(t *testing.T) {
	const dashed = "-synthetic-workspace-dashed/session"
	for _, tc := range []struct {
		name     string
		args     []string
		operands []string
		host     string
		asJSON   bool
		err      string
		help     bool
	}{
		{name: "a dash-leading operand is an operand", args: []string{dashed}, operands: []string{dashed}},
		{name: "boolean flag after the operand", args: []string{dashed, "--json"}, operands: []string{dashed}, asJSON: true},
		{name: "boolean flag before the operand", args: []string{"--json", dashed}, operands: []string{dashed}, asJSON: true},
		{name: "a value flag claims a dash-leading value", args: []string{"--host", dashed}, host: dashed},
		{name: "value flag after the operand", args: []string{dashed, "--host", "synthetic"}, operands: []string{dashed}, host: "synthetic"},
		{name: "the equals form needs no following token", args: []string{"--host=-synthetic", dashed}, operands: []string{dashed}, host: "-synthetic"},
		{name: "the terminator protects every operand after it", args: []string{"--", "--json", dashed}, operands: []string{"--json", dashed}},
		{name: "a lone dash is an operand", args: []string{"-"}, operands: []string{"-"}},
		{name: "a boolean flag does not eat the next token", args: []string{"--json", "false"}, operands: []string{"false"}, asJSON: true},
		{name: "an undefined long name is a flag", args: []string{"--jsn", dashed}, err: "flag provided but not defined"},
		{name: "malformed syntax stays the flag package's finding", args: []string{"---json"}, err: "bad flag syntax"},
		{name: "a value flag with no value is still rejected", args: []string{"--host"}, err: "needs an argument"},
		{name: "a value flag with no value is rejected after an operand", args: []string{dashed, "--host"}, err: "needs an argument"},
		{name: "help is served, not parsed", args: []string{"-h"}, help: true},
	} {
		c := newCmd("test", "Usage: synthetic\n")
		host := c.fs.String("host", "", "a value flag")
		asJSON := c.fs.Bool("json", false, "a boolean flag")
		var stdout bytes.Buffer
		a := &app{stdout: &stdout, stderr: &bytes.Buffer{}}

		err := c.parse(a, tc.args)
		switch {
		case tc.help:
			if !errors.Is(err, errHelp) {
				t.Fatalf("%s: parse returned %v, want errHelp", tc.name, err)
			}
			if !strings.Contains(stdout.String(), "Usage: synthetic") {
				t.Fatalf("%s: help served %q", tc.name, stdout.String())
			}
			continue
		case tc.err != "":
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Fatalf("%s: parse returned %v, want a usage error containing %q", tc.name, err, tc.err)
			}
			if !strings.Contains(ue.msg, tc.err) {
				t.Fatalf("%s: parse reported %q, want it to contain %q", tc.name, ue.msg, tc.err)
			}
			continue
		case err != nil:
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		if !slices.Equal(c.args(), tc.operands) {
			t.Fatalf("%s: operands = %q, want %q", tc.name, c.args(), tc.operands)
		}
		if *host != tc.host {
			t.Fatalf("%s: --host = %q, want %q", tc.name, *host, tc.host)
		}
		if *asJSON != tc.asJSON {
			t.Fatalf("%s: --json = %v, want %v", tc.name, *asJSON, tc.asJSON)
		}
	}
}

// The continuation grade decides whether a session can be resumed, and the
// terminal was the one surface that could not report it: --json and the web
// interface both carry it, so a human listing without it forced an inspect
// per session to learn what the listing already knew.
func TestSessionsListShowsTheContinuationGrade(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	f.writeDashSession()

	stdout, _ := f.ok("sessions", "list", "--json")
	rows := decode[sessionsResult](t, stdout).Sessions
	if len(rows) == 0 {
		t.Fatal("the fixture listed no sessions")
	}
	want := make(map[string]string, len(rows))
	for _, row := range rows {
		want[row.SourceID] = gradeCell(row.Continuous)
	}

	stdout, _ = f.ok("sessions", "list")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if !strings.Contains(lines[0], "GRADE") {
		t.Fatalf("the human listing has no GRADE column: %q", lines[0])
	}
	if len(lines)-1 != len(rows) {
		t.Fatalf("the human listing has %d rows, want %d\n%s", len(lines)-1, len(rows), stdout)
	}
	// The source id is the second column and the grade the last, and neither
	// carries whitespace, so each row states its own grade.
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		sourceID, grade := fields[1], fields[len(fields)-1]
		if grade != want[sourceID] {
			t.Fatalf("the listing graded %s %q, want %q (from --json)\n%s", sourceID, grade, want[sourceID], stdout)
		}
	}

	// An unobserved grade stays absent rather than rendering as "no": an
	// archive listing reads no transcript, so it graded nothing (SPEC.md §3).
	if got := gradeCell(nil); got != missingValue {
		t.Fatalf("an unobserved grade rendered as %q, want %q", got, missingValue)
	}
}

// Fetch mirrors each recorded absolute source path beneath the target, which
// keeps two harnesses' same-named files apart but means the source paths it
// requested are not paths an operator can open. So the outcome names where
// every file landed, and the three lists stay reconcilable: everything asked
// for is either somewhere on disk or accounted absent from the snapshot.
func TestFetchReportsWhereTheFilesLanded(t *testing.T) {
	f := newFixture(t).withRepo()
	primary := f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)

	stdout, _ := f.ok(f.with("sessions", "fetch", richSessionStem, "--json")...)
	res := decode[fetchResult](t, stdout)

	// Extracted so the same accounting is checked on a complete recovery and
	// on a partial one: the invariant is what catches a future fetch that
	// reports the two lists inconsistently.
	assertAccountedFor := func(what string, res fetchResult) {
		if len(res.Restored)+len(res.Missing) != len(res.Included) {
			t.Fatalf("%s: restored %d + missing %d does not account for the %d requested closure paths: %+v",
				what, len(res.Restored), len(res.Missing), len(res.Included), res)
		}
		if len(res.Restored) == 0 {
			t.Fatalf("%s: fetch reported nothing restored: %+v", what, res)
		}
		for _, path := range res.Restored {
			// A reported path is where the file is, not the source path it
			// mirrors: a source path exists on this machine too, so only the
			// target prefix distinguishes the two.
			if !strings.HasPrefix(path, res.Target+string(os.PathSeparator)) {
				t.Fatalf("%s: restored path %q is not under the target %q", what, path, res.Target)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("%s: restored path %q does not exist on disk: %v", what, path, err)
			}
		}
		// The mirror rule is stated once, in the result, so that no caller
		// has to derive it.
		for _, included := range res.Included {
			landed := filepath.Join(res.Target, included)
			switch {
			case slices.Contains(res.Missing, included):
				if slices.Contains(res.Restored, landed) {
					t.Fatalf("%s: %q is reported both absent and restored at %q", what, included, landed)
				}
			case !slices.Contains(res.Restored, landed):
				t.Fatalf("%s: included path %q landed at %q, which is not reported: %q", what, included, landed, res.Restored)
			}
		}
		if landed := filepath.Join(res.Target, primary); !slices.Contains(res.Restored, landed) {
			t.Fatalf("%s: the restored primary log %q is not reported: %q", what, landed, res.Restored)
		}
	}
	assertAccountedFor("complete recovery", res)
	if len(res.Missing) != 0 {
		t.Fatalf("the whole closure was archived, yet %q is reported absent", res.Missing)
	}

	// A file written after the snapshot was taken is in the live closure and
	// not in the archive, which is the partial recovery the accounting has to
	// stay honest about: it is named absent, and it is not claimed as landed.
	late := filepath.Join(strings.TrimSuffix(primary, ".jsonl"), "nested", "8.bash.log")
	if err := os.WriteFile(late, []byte("synthetic output written after the push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ = f.ok(f.with("sessions", "fetch", richSessionStem, "--json")...)
	partial := decode[fetchResult](t, stdout)
	if !slices.Contains(partial.Included, late) {
		t.Fatalf("the late artifact %q is not in the requested closure: %q", late, partial.Included)
	}
	if !slices.Contains(partial.Missing, late) {
		t.Fatalf("the late artifact %q is not reported absent from the snapshot: %q", late, partial.Missing)
	}
	assertAccountedFor("partial recovery", partial)

	// The human surface answers the same question: the second fetch finds the
	// target already materialized and still says where its files are.
	stdout, _ = f.ok(f.with("sessions", "fetch", richSessionStem)...)
	if landed := filepath.Join(res.Target, primary); !strings.Contains(stdout, landed) {
		t.Fatalf("the human fetch output does not name where the primary log landed (%q):\n%s", landed, stdout)
	}
}
