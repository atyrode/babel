package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/title"
)

// titlesWorker writes a stub Code executable that answers both launches this
// feature makes, because on a configured machine they are one executable: the
// configuration ceremony (--configure --result-file PATH) and the titler run
// (--titles --profile ID@REVISION). Every launch appends what it was actually
// given to record, so a test asserts the argv and the environment Babel used
// rather than the ones it meant to use.
//
// It is a script rather than a compiled helper for the same reason
// ceremonyWorker is: what has to be observed is the launch itself — the argv,
// the inherited streams, and the environment — and a shell answers all three
// directly.
func titlesWorker(t *testing.T, payload string, code int) (binary, record string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "stub-code")
	record = filepath.Join(dir, "launch")
	answer := ""
	if payload != "" {
		answer = `if [ -n "$result" ]; then printf '%s' '` + payload + `' >"$result"; fi`
	}
	script := strings.NewReplacer(
		"@RECORD@", record,
		"@ANSWER@", answer,
		"@CODE@", strconv.Itoa(code),
	).Replace(`#!/bin/sh
record='@RECORD@'
{
	printf 'argv: %s\n' "$*"
	printf 'selection: %s\n' "${CODE_SELECTION_STATE-unset}"
	printf 'home: %s\n' "${HOME-unset}"
} >>"$record"
mode='titles'
result=''
while [ $# -gt 0 ]; do
	case "$1" in
	--configure) mode='configure' ;;
	--result-file) shift; result="$1" ;;
	esac
	shift
done
if [ "$mode" = 'configure' ]; then
	# The stream tests are outside the redirection above: inside it, fd 1 is
	# the record file and every answer would be "not a terminal".
	for fd in 0 1 2; do
		if [ -t "$fd" ]; then
			printf 'fd%s: terminal\n' "$fd" >>"$record"
		else
			printf 'fd%s: not a terminal\n' "$fd" >>"$record"
		fi
	done
	@ANSWER@
	exit @CODE@
fi
# Titler mode: one JSON response per request line, echoing back the selector
# Babel sent so a test can prove which sessions were offered.
while IFS= read -r line; do
	rest=${line#*\"selector\":\"}
	selector=${rest%%\"*}
	printf 'titled: %s\n' "$selector" >>"$record"
	printf '{"selector":"%s","title":"a model wrote this one","model":"stub-model"}\n' "$selector"
done
exit 0
`)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, record
}

// untitledSessionStem names the fixture session the harness gave no title.
// It is the only kind title inference may offer: a recorded title is never
// replaced, because Babel's guess does not outrank the session's own record.
const untitledSessionStem = "2026-02-01T00-00-00-000Z_00000000-0000-4000-8000-00000000009a"

// untitledSession materializes that session.
func untitledSession(f *fixture) {
	f.t.Helper()
	f.writeSession(sessionSpec{
		project:   "synthetic-untitled",
		stem:      untitledSessionStem,
		id:        "00000000-0000-4000-8000-00000000009a",
		workspace: "/synthetic/workspace/untitled",
		message:   "the operator asked for a summary of this session",
	})
}

// untitledSelector returns that session's selector, taken from the listing so
// a test addresses it with the identifier the tool itself hands out.
func untitledSelector(t *testing.T, f *fixture) string {
	t.Helper()
	stdout, _ := f.ok("sessions", "list", "--json")
	for _, row := range decode[sessionsResult](t, stdout).Sessions {
		if strings.Contains(row.SourceID, untitledSessionStem) {
			return row.Selector
		}
	}
	t.Fatalf("the fixture's untitled session was not listed:\n%s", stdout)
	return ""
}

// listedSession returns one session's row as the listing renders it, which is
// where an inferred title becomes visible to an operator.
func listedSession(t *testing.T, f *fixture, selector string) sessionRow {
	t.Helper()
	stdout, _ := f.ok("sessions", "list", "--json")
	for _, row := range decode[sessionsResult](t, stdout).Sessions {
		if row.Selector == selector {
			return row
		}
	}
	t.Fatalf("session %q is not in the listing:\n%s", selector, stdout)
	return sessionRow{}
}

// inferredRows is the durable inferred-title store as it is on disk, which is
// the only authority on whether a spend produced anything.
func inferredRows(t *testing.T, f *fixture) map[string]title.Inferred {
	t.Helper()
	store, err := title.Open(f.dataDir)
	if err != nil {
		t.Fatalf("opening the durable title store: %v", err)
	}
	defer store.Close()
	all, err := store.All(context.Background())
	if err != nil {
		t.Fatalf("reading the durable title store: %v", err)
	}
	return all
}

// seedInferredTitle plants a title an earlier operator already paid for.
func seedInferredTitle(t *testing.T, f *fixture, selector, text string) {
	t.Helper()
	store, err := title.Open(f.dataDir)
	if err != nil {
		t.Fatalf("opening the durable title store: %v", err)
	}
	defer store.Close()
	if err := store.Put(context.Background(), title.Inferred{
		Selector:   selector,
		Title:      text,
		Titler:     "/opt/code/code earlier-profile@3",
		Model:      "a model from before the ceremony",
		InferredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seeding an inferred title: %v", err)
	}
}

// TestTitlesConfigureRefusesWithoutATerminal is decision 2 of issue #86
// meeting decision 1's rule: the profile that writes titles is chosen by an
// operator watching Code offer it, so an invocation with nowhere to display
// Code's interface must not launch anything at all.
//
// The three cases are the three ways the streams can fail to be a terminal,
// and the null device matters most: it is a character device, so a mode-based
// terminal test accepts it and would hand an interactive configuration to
// nothing.
func TestTitlesConfigureRefusesWithoutATerminal(t *testing.T) {
	newFixture(t)
	binary, record := titlesWorker(t, `{"profile":"titles-chosen","revision":4}`, 0)

	cases := []struct {
		name string
		open func(*testing.T) (io.Reader, io.Writer)
	}{
		{"captured buffers", func(*testing.T) (io.Reader, io.Writer) {
			return strings.NewReader(""), &bytes.Buffer{}
		}},
		{"a pipe", func(t *testing.T) (io.Reader, io.Writer) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.Close(); w.Close() })
			return r, w
		}},
		{"the null device", func(t *testing.T) (io.Reader, io.Writer) {
			in, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { in.Close(); out.Close() })
			return in, out
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := tc.open(t)
			var stderr bytes.Buffer
			code := run([]string{"titles", "configure", "--worker", binary}, in, out, &stderr)
			if code != exitFailure {
				t.Fatalf("a terminal-less invocation exited %d, want %d\nstderr: %s", code, exitFailure, stderr.String())
			}
			if got := strings.Count(strings.TrimSuffix(stderr.String(), "\n"), "\n"); got != 0 {
				t.Errorf("the refusal is %d lines, want one:\n%s", got+1, stderr.String())
			}
			// The refusal has to say what is missing and what the missing
			// configuration costs, because "no terminal" alone reads as a bug.
			for _, want := range []string{"needs a terminal", "babel sessions title infer --confirm"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("the refusal does not mention %q:\n%s", want, stderr.String())
				}
			}
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Error("the worker was launched by an invocation that had no terminal to launch it on")
			}
			path, err := analysisPath()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Error("the refusal wrote a settings document")
			}
		})
	}
}

// TestTitlesConfigureHandsTheTerminalToCode is the ceremony end to end against
// a real pty: the stub is launched in configuration mode with the operator's
// terminal on all three streams, without the environment dial that could have
// answered for him, and the reference it writes is what Babel stores.
//
// It also holds the boundary between the two configurations. An analysis
// profile is a different intentional setup, and this ceremony must come out of
// it untouched — otherwise configuring one silently decides the other, which
// is the thing issue #86 removes.
func TestTitlesConfigureHandsTheTerminalToCode(t *testing.T) {
	f := newFixture(t)
	if _, err := saveAnalysisSettings(analysisSettings{
		Worker:     "/other/code",
		WorkerArgs: []string{"analysis-mode"},
		Profile:    &profileRecord{ID: "analysis-chosen", Revision: 2, ConfiguredAt: "2026-08-30T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}

	term := openTerminal(t)
	binary, record := titlesWorker(t, `{"profile":"titles-chosen","revision":4}`, 0)
	// The dial an operator's shell might export. It is not intent, so it must
	// not reach the worker.
	t.Setenv("CODE_SELECTION_STATE", "model=haiku;effort=low")

	var stderr bytes.Buffer
	code := run([]string{"titles", "configure", "--worker", binary, "--worker-arg", "babel"},
		term.slave, term.slave, &stderr)
	displayed := term.collect(t)
	if code != exitOK {
		t.Fatalf("the ceremony exited %d\nstderr: %s\nterminal: %s", code, stderr.String(), displayed)
	}

	launch := launchRecord(t, record)
	for _, want := range []string{
		// The worker's own arguments first, then the two flags Babel owns:
		// Code is put into its mode, then told where to answer.
		"argv: babel --configure --result-file ",
		"fd0: terminal",
		"fd1: terminal",
		"fd2: terminal",
		"selection: unset",
		"home: " + f.home,
	} {
		if !strings.Contains(launch, want) {
			t.Errorf("the launch does not show %q:\n%s", want, launch)
		}
	}

	settings := storedSettings(t)
	titles := settings.Titles
	switch {
	case titles == nil:
		t.Fatalf("the confirmed reference was not stored:\n%s", settingsBytes(t))
	case titles.Profile != "titles-chosen":
		t.Errorf("stored profile = %q, want the one the worker wrote", titles.Profile)
	case titles.Revision != 4:
		t.Errorf("stored revision = %d, want 4", titles.Revision)
	case titles.ConfiguredAt == "":
		t.Error("the stored reference does not record when it was configured")
	case titles.Worker != binary || !slices.Equal(titles.WorkerArgs, []string{"babel"}):
		t.Errorf("stored launch = %q %v, want the one that was used", titles.Worker, titles.WorkerArgs)
	}
	// The analysis block is another ceremony's answer and not this command's
	// to touch.
	if settings.Profile == nil || settings.Profile.ID != "analysis-chosen" ||
		settings.Worker != "/other/code" {
		t.Errorf("the titles ceremony rewrote the analysis configuration:\n%s", settingsBytes(t))
	}

	// The summary states the reference and the launch inference will make,
	// because "configured" without either is not a fact an operator can check.
	for _, want := range []string{"titles-chosen", "4", "--titles --profile titles-chosen@4"} {
		if !strings.Contains(displayed, want) {
			t.Errorf("the summary on the terminal does not show %q:\n%s", want, displayed)
		}
	}
	for _, want := range []string{"handing this terminal", "ignoring $CODE_SELECTION_STATE"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the diagnostics do not mention %q:\n%s", want, stderr.String())
		}
	}
	// The reference travelled through a file Babel owned and removed.
	if _, err := os.Stat(filepath.Dir(resultFileArg(t, launch))); !errors.Is(err, os.ErrNotExist) {
		t.Error("the result file outlived the ceremony")
	}

	// And the reverse direction of the same boundary: the analysis ceremony
	// leaves the titles reference exactly where it was.
	second := openTerminal(t)
	analysis, _ := ceremonyWorker(t, `{"profile":"analysis-rechosen","revision":9}`, 0)
	var analysisErr bytes.Buffer
	if code := run([]string{"analysis", "profile", "configure", "--worker", analysis},
		second.slave, second.slave, &analysisErr); code != exitOK {
		t.Fatalf("the analysis ceremony exited %d: %s", code, analysisErr.String())
	}
	second.collect(t)
	after := storedSettings(t)
	if after.Titles == nil || after.Titles.Profile != "titles-chosen" || after.Titles.Worker != binary {
		t.Errorf("the analysis ceremony changed the titles reference:\n%s", settingsBytes(t))
	}
}

// TestTitlesConfigureLeavesTheStoredReferenceAlone holds the other half of the
// ceremony's contract: every way it can end without a confirmed reference ends
// with the machine configured exactly as it was, said out loud, and a nonzero
// exit. A half-applied configuration is the failure that matters here — the
// operator would have no way to tell which model the next inference uses.
func TestTitlesConfigureLeavesTheStoredReferenceAlone(t *testing.T) {
	newFixture(t)
	confirmed, _ := titlesWorker(t, `{"profile":"titles-chosen","revision":4}`, 0)
	first := openTerminal(t)
	var stderr bytes.Buffer
	if code := run([]string{"titles", "configure", "--worker", confirmed},
		first.slave, first.slave, &stderr); code != exitOK {
		t.Fatalf("storing the initial reference exited %d: %s", code, stderr.String())
	}
	first.collect(t)
	before := settingsBytes(t)

	cases := []struct {
		name    string
		payload string
		code    int
		reason  string
	}{
		// A worker that wrote a reference and then failed is the sharpest
		// case: the file says one thing and the exit status another, and the
		// exit status wins.
		{"the worker exits nonzero", `{"profile":"agent-minted","revision":9}`, 7, "exited 7"},
		{"the worker writes nothing", "", 0, "wrote no reference"},
		{"the worker writes something else", "not a document at all", 0, "not the JSON Babel reads"},
		{"the result names no profile", `{"revision":9}`, 0, "names no profile"},
		{"the revision is not positive", `{"profile":"agent-minted","revision":0}`, 0, "not a positive integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := openTerminal(t)
			binary, _ := titlesWorker(t, tc.payload, tc.code)
			var stderr bytes.Buffer
			code := run([]string{"titles", "configure", "--worker", binary},
				term.slave, term.slave, &stderr)
			displayed := term.collect(t)
			if code != exitFailure {
				t.Fatalf("an abandoned ceremony exited %d, want %d\nstderr: %s", code, exitFailure, stderr.String())
			}
			// The remedy names this configuration's own show command: an
			// operator sent to the other one would read a profile that has
			// nothing to do with the ceremony he just abandoned.
			for _, want := range []string{"configuration unchanged", tc.reason, "babel titles show"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("the report does not mention %q:\n%s", want, stderr.String())
				}
			}
			if strings.Contains(stderr.String(), "babel analysis profile show") {
				t.Errorf("the report sent the operator to the other configuration:\n%s", stderr.String())
			}
			if got := settingsBytes(t); got != before {
				t.Errorf("the stored configuration changed:\nbefore: %s\nafter:  %s", before, got)
			}
			if strings.Contains(displayed, "agent-minted") {
				t.Errorf("a reference that was never accepted was displayed as stored:\n%s", displayed)
			}
		})
	}
}

// TestTitleInferenceRefusesUntilAnOperatorConfiguresIt is decision 2 at the
// point it bites. An unconfigured machine has no model for this, so --confirm
// refuses, names the one command that chooses one, and sends nothing.
//
// The rest of the test is the boundary of that refusal. It is a gate on new
// spend, not a withdrawal: a title an earlier operator paid for keeps its
// value and its honest "inferred" provenance, and the disclosure preview -
// which reads local files and sends nothing - still works, because the
// operator deciding whether to configure this at all is exactly the person who
// needs to see what it would send.
func TestTitleInferenceRefusesUntilAnOperatorConfiguresIt(t *testing.T) {
	f := newFixture(t)
	untitledSession(f)
	selector := untitledSelector(t, f)
	const earlier = "a title from before the ceremony"
	seedInferredTitle(t, f, selector, earlier)

	_, stderr := f.mustExit(exitFailure, "sessions", "title", "infer", "--confirm")
	for _, want := range []string{
		"no title-inference profile is configured",
		"babel titles configure",
		"nothing was sent",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, stderr)
		}
	}

	rows := inferredRows(t, f)
	if len(rows) != 1 || rows[selector].Title != earlier {
		t.Errorf("the refusal changed the durable store: %+v", rows)
	}
	row := listedSession(t, f, selector)
	if row.Title == nil || *row.Title != earlier {
		t.Errorf("the earlier inferred title stopped displaying: %v", row.Title)
	}
	if row.TitleProvenance == nil || *row.TitleProvenance != "inferred" {
		t.Errorf("title provenance = %v, want the honest \"inferred\"", row.TitleProvenance)
	}

	// The preview still runs, states that nothing is configured, and names the
	// ceremony rather than --confirm.
	stdout, _ := f.ok("sessions", "title", "infer")
	for _, want := range []string{
		"no title-inference profile is configured",
		"babel titles configure",
		"this material would leave the machine",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the preview does not mention %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "re-run with --confirm to send it") {
		t.Errorf("the preview offered --confirm on a machine where it would refuse:\n%s", stdout)
	}

	// And the escape hatch is closed: naming a command on the command line was
	// how a model got chosen without anyone choosing it.
	_, stderr = f.mustExit(exitUsage, "sessions", "title", "infer",
		"--titler", "/bin/echo", "--confirm")
	if !strings.Contains(stderr, "titler") {
		t.Errorf("the removed flag was not rejected by name:\n%s", stderr)
	}
	if len(inferredRows(t, f)) != 1 {
		t.Error("a rejected invocation reached the durable store")
	}
}

// TestTitleInferenceUsesExactlyTheStoredReference is the other half: once an
// operator has sat through the ceremony, inference launches what he confirmed
// and nothing else - that executable, those arguments, that profile - and the
// title it records says which launch produced it.
func TestTitleInferenceUsesExactlyTheStoredReference(t *testing.T) {
	f := newFixture(t)
	untitledSession(f)
	term := openTerminal(t)
	binary, record := titlesWorker(t, `{"profile":"titles-chosen","revision":4}`, 0)
	t.Setenv("CODE_SELECTION_STATE", "model=haiku;effort=low")

	var stderr bytes.Buffer
	if code := run([]string{"titles", "configure", "--worker", binary, "--worker-arg", "babel"},
		term.slave, term.slave, &stderr); code != exitOK {
		t.Fatalf("the ceremony exited %d: %s\nterminal: %s", code, stderr.String(), term.collect(t))
	}
	term.collect(t)

	selector := untitledSelector(t, f)
	stdout, diagnostics := f.ok("sessions", "title", "infer", "--confirm")

	launch := launchRecord(t, record)
	// The titler launch: the stored arguments, then the mode and the reference
	// the operator confirmed. No flag on this invocation contributed to it.
	if !strings.Contains(launch, "argv: babel --titles --profile titles-chosen@4") {
		t.Errorf("the titler was not launched with the stored reference:\n%s", launch)
	}
	if !strings.Contains(launch, "titled: "+selector) {
		t.Errorf("the titler was not offered %q:\n%s", selector, launch)
	}
	// Both launches, the ceremony's and the titler's, are free of the dial.
	if got := strings.Count(launch, "selection: unset"); got != 2 {
		t.Errorf("%d of 2 launches ran without $CODE_SELECTION_STATE:\n%s", got, launch)
	}
	if !strings.Contains(diagnostics, "ignoring $CODE_SELECTION_STATE") {
		t.Errorf("the titler run did not say it dropped the dial:\n%s", diagnostics)
	}

	rows := inferredRows(t, f)
	stored, ok := rows[selector]
	switch {
	case !ok:
		t.Fatalf("no title was recorded for %q: %+v", selector, rows)
	case stored.Title != "a model wrote this one":
		t.Errorf("recorded title = %q, want the one the titler returned", stored.Title)
	case stored.Model != "stub-model":
		t.Errorf("recorded model = %q, want the identity the titler reported", stored.Model)
	}
	// Attribution survives a later reconfiguration, so it carries the launch
	// rather than pointing at a document that can change under it.
	for _, want := range []string{binary, "titles-chosen@4"} {
		if !strings.Contains(stored.Titler, want) {
			t.Errorf("recorded titler %q does not name %q", stored.Titler, want)
		}
	}
	if !strings.Contains(stdout, "a model wrote this one") {
		t.Errorf("the outcome does not report the title that was recorded:\n%s", stdout)
	}

	row := listedSession(t, f, selector)
	if row.Title == nil || *row.Title != "a model wrote this one" ||
		row.TitleProvenance == nil || *row.TitleProvenance != "inferred" {
		t.Errorf("the listing does not show the inferred title: %v/%v", row.Title, row.TitleProvenance)
	}

	// The disclosure names the profile the material would go to, which is the
	// fact that decides whether sending it is acceptable.
	stdout, _ = f.ok("sessions", "title", "infer")
	if !strings.Contains(stdout, "titles-chosen@4") {
		t.Errorf("the preview does not name the profile it would run under:\n%s", stdout)
	}
}

// TestTitlesShowPrintsWhatWasStoredAndNothingElse: show is how an operator
// checks which model his machine will use, so it must state the reference, the
// launch it implies, and - when there is none - that inference refuses.
func TestTitlesShowPrintsWhatWasStoredAndNothingElse(t *testing.T) {
	f := newFixture(t)

	stdout, _ := f.ok("titles", "show")
	for _, want := range []string{
		"no title-inference profile is stored",
		"babel titles configure",
		"babel sessions title infer --confirm",
		"titles already inferred are unaffected",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the unconfigured report does not mention %q:\n%s", want, stdout)
		}
	}
	stdout, _ = f.ok("titles", "show", "--json")
	res := decode[titlesResult](t, stdout)
	if res.Configured || res.Titler != nil || len(res.Launch) != 0 {
		t.Errorf("an unconfigured machine reported a configuration: %+v", res)
	}
	if res.Owner != profileOwner || !strings.HasSuffix(res.Path, analysisFile) {
		t.Errorf("show does not name the owner and the document: %+v", res)
	}

	if _, err := saveAnalysisSettings(analysisSettings{
		Titles: &titlesRecord{
			Profile:      "titles-chosen",
			Revision:     4,
			ConfiguredAt: "2026-08-31T00:00:00Z",
			Worker:       "/opt/code/code",
			WorkerArgs:   []string{"babel"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _ = f.ok("titles", "show")
	for _, want := range []string{
		"titles-chosen",
		"2026-08-31T00:00:00Z",
		"/opt/code/code babel --titles --profile titles-chosen@4",
		"SPEC.md §2.6",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the stored reference does not render %q:\n%s", want, stdout)
		}
	}
	stdout, _ = f.ok("titles", "show", "--json")
	res = decode[titlesResult](t, stdout)
	if !res.Configured || res.Titler == nil || res.Titler.Profile != "titles-chosen" || res.Titler.Revision != 4 {
		t.Fatalf("show did not report the stored reference: %+v", res)
	}
	want := []string{"/opt/code/code", "babel", "--titles", "--profile", "titles-chosen@4"}
	if !slices.Equal(res.Launch, want) {
		t.Errorf("launch = %v, want %v", res.Launch, want)
	}

	// A document on disk is a producer's output like any other (SPEC.md §3),
	// and this one names an executable. A path carrying an escape sequence
	// must render inert rather than reach the terminal.
	if _, err := saveAnalysisSettings(analysisSettings{
		Titles: &titlesRecord{
			Profile: hostileTitle, Revision: 1, ConfiguredAt: "2026-08-31T00:00:00Z",
			Worker: "/opt/" + hostileTitle,
		},
	}); err != nil {
		t.Fatal(err)
	}
	stdout, _ = f.ok("titles", "show")
	for _, raw := range []string{"\x1b", "\a", "\u202e"} {
		if strings.Contains(stdout, raw) {
			t.Errorf("a hostile stored value reached the terminal raw:\n%q", stdout)
		}
	}
}
