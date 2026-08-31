package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/worker"
)

// unqualifiedPlatform stands in for the refusal internal/worker produces when
// the host platform has no backend that has passed its escape scenario. The real
// one is only constructible on such a host, and the platform under test must not
// be whichever machine runs the suite — that is exactly the coupling this test
// exists to keep out of the report.
type unqualifiedPlatform struct{ goos string }

func (p unqualifiedPlatform) UnqualifiedPlatform() string { return p.goos }

func (p unqualifiedPlatform) Error() string {
	return fmt.Sprintf("%s: exploration is refused on %s because no backend has passed its escape scenario there; the worker declared backend %q",
		worker.ErrPlatformUnqualified, p.goos, "process")
}

func (p unqualifiedPlatform) Is(target error) bool {
	return target == worker.ErrContainment || target == worker.ErrPlatformUnqualified
}

// reportedFailure runs the operator-facing failure report for err and returns
// what an operator would read on stderr.
func reportedFailure(t *testing.T, err error) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	if got := a.reportWorkerFailure("/opt/code/code", err); !errors.Is(got, errReported) {
		t.Fatalf("reportWorkerFailure returned %v, want errReported", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("a refusal wrote to stdout, which carries machine-readable output: %q", stdout.String())
	}
	return stderr.String()
}

// TestPlatformRefusalReachesTheOperator is the wiring that makes §10's limit
// legible. `babel explore` reports a failed run through reportWorkerFailure, so
// unless the platform case is routed there the operator reads a heading that
// blames the worker and a remedy that does not exist.
//
// The assertions are about which account the operator gets, not its wording: the
// platform report must attribute the refusal to the platform the refusal names,
// must not present the worker as the thing that failed, and must say what the
// machine still does.
func TestPlatformRefusalReachesTheOperator(t *testing.T) {
	// The shape internal/explore hands up: the stage wraps the worker's error,
	// so the report has to see through the wrapping. The platform is a name no
	// host can have, so any appearance of the running machine's GOOS below is
	// the renderer having consulted itself instead of the refusal.
	const refused = "unqualified-test-platform"
	err := fmt.Errorf("explore: explore job: %w", unqualifiedPlatform{goos: refused})
	out := reportedFailure(t, err)

	if !strings.Contains(out, refused) {
		t.Errorf("the refusal does not name the platform it applies to:\n%s", out)
	}
	if strings.Contains(out, runtime.GOOS) {
		t.Errorf("the report named the machine it is running on (%s) rather than the platform the refusal is about:\n%s",
			runtime.GOOS, out)
	}
	if strings.Contains(out, "the Code analysis worker could not run") {
		t.Errorf("a platform limit was reported as a worker fault:\n%s", out)
	}
	// Refusing exploration is not refusing Babel; an operator who cannot
	// explore here still needs to know the archive works.
	for _, works := range []string{"archive", "review", "sessions", "web"} {
		if !strings.Contains(out, works) {
			t.Errorf("the refusal does not say that %s still works here:\n%s", works, out)
		}
	}
	if !strings.Contains(out, "exploration is refused") {
		t.Errorf("the refusal does not say that exploration is what is refused:\n%s", out)
	}
}

// TestWorkerShortfallIsNotReportedAsAPlatformLimit is the other half. A worker
// that declares too little on a qualified platform has a remedy in the worker,
// and reporting it as a platform limit would tell the operator their machine
// cannot explore at all — false, and it would hide the property list that names
// the actual fix.
func TestWorkerShortfallIsNotReportedAsAPlatformLimit(t *testing.T) {
	err := fmt.Errorf("explore: explore job: %w: backend %q does not provide resource ceilings",
		worker.ErrContainment, "bwrap+systemd-scope")
	out := reportedFailure(t, err)

	if !strings.Contains(out, "resource ceilings") {
		t.Errorf("the property-level diagnostic was lost:\n%s", out)
	}
	if !strings.Contains(out, "the Code analysis worker could not run") {
		t.Errorf("a worker shortfall was not attributed to the worker:\n%s", out)
	}
	if strings.Contains(out, "no qualified") || strings.Contains(out, "has passed its escape scenario") {
		t.Errorf("a fixable worker was reported as an unqualified platform:\n%s", out)
	}
}

// ceremonyWorker writes a stub worker that records how Babel launched it and
// then answers the way Code's configuration mode does: it writes payload to
// the result file Babel named and exits with code. An empty payload writes
// nothing, which is what a session the operator backed out of looks like from
// Babel's side.
//
// It is a script rather than a compiled helper because what has to be observed
// is the launch itself — the argv, the three inherited streams, and the
// environment — and a shell answers all three questions directly.
func ceremonyWorker(t *testing.T, payload string, code int) (binary, record string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "stub-code")
	record = filepath.Join(dir, "launch")
	answer := ""
	if payload != "" {
		answer = fmt.Sprintf("if [ -n \"$result\" ]; then printf '%%s' '%s' >\"$result\"; fi\n", payload)
	}
	script := fmt.Sprintf(`#!/bin/sh
record='%s'
{
	printf 'argv: %%s\n' "$*"
	printf 'selection: %%s\n' "${CODE_SELECTION_STATE-unset}"
	printf 'home: %%s\n' "${HOME-unset}"
} >>"$record"
# Each stream is tested outside that redirection: inside it, fd 1 is the
# record file and every answer would be "not a terminal".
for fd in 0 1 2; do
	if [ -t "$fd" ]; then
		printf 'fd%%s: terminal\n' "$fd" >>"$record"
	else
		printf 'fd%%s: not a terminal\n' "$fd" >>"$record"
	fi
done
result=''
while [ $# -gt 0 ]; do
	if [ "$1" = '--result-file' ]; then
		shift
		result="$1"
	fi
	shift
done
%sexit %d
`, record, answer, code)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, record
}

// launchRecord returns what the stub worker recorded about its launch.
func launchRecord(t *testing.T, record string) string {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the worker recorded no launch: %v", err)
	}
	return string(data)
}

// resultFileArg returns the result-file path Babel passed, read back out of
// the recorded argv.
func resultFileArg(t *testing.T, launch string) string {
	t.Helper()
	fields := strings.Fields(launch)
	for i, field := range fields {
		if field == "--result-file" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	t.Fatalf("the recorded launch names no result file:\n%s", launch)
	return ""
}

// storedSettings decodes the settings document as it is on disk.
func storedSettings(t *testing.T) analysisSettings {
	t.Helper()
	settings, err := loadAnalysisSettings()
	if err != nil {
		t.Fatalf("reading the stored settings: %v", err)
	}
	return settings
}

// settingsBytes is the settings document verbatim, which is how "unchanged" is
// asserted: a re-serialized comparison would hide a field that was rewritten
// to the same value by a command that had no business writing at all.
func settingsBytes(t *testing.T) string {
	t.Helper()
	path, err := analysisPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// TestProfileConfigureRefusesWithoutATerminal is the refusal decision 1 of
// issue #86 turns on: a profile is minted by an operator watching Code mint
// it, so an invocation with nowhere to display Code's interface must not
// launch anything at all.
//
// The three cases are the three ways the streams can fail to be a terminal,
// and the null device is the one that matters most: it is a character device,
// so a mode-based terminal test accepts it and would hand an interactive
// configuration to nothing.
func TestProfileConfigureRefusesWithoutATerminal(t *testing.T) {
	newFixture(t)
	binary, record := ceremonyWorker(t, `{"profile":"operator-chosen","revision":4}`, 0)

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
			code := run([]string{"analysis", "profile", "configure", "--worker", binary}, in, out, &stderr)
			if code != exitFailure {
				t.Fatalf("a terminal-less invocation exited %d, want %d\nstderr: %s", code, exitFailure, stderr.String())
			}
			if got := strings.Count(strings.TrimSuffix(stderr.String(), "\n"), "\n"); got != 0 {
				t.Errorf("the refusal is %d lines, want one:\n%s", got+1, stderr.String())
			}
			for _, want := range []string{"needs a terminal", "babel explore --profile"} {
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

// TestProfileConfigureHandsTheTerminalToCode is the ceremony end to end
// against a real pty: the worker is launched in configuration mode with the
// operator's terminal on all three streams, without the environment dial that
// used to be able to answer for them, and the reference it writes is what
// Babel stores.
func TestProfileConfigureHandsTheTerminalToCode(t *testing.T) {
	f := newFixture(t)
	term := openTerminal(t)
	binary, record := ceremonyWorker(t, `{"profile":"operator-chosen","revision":4}`, 0)
	// The dial an operator's shell might export. It is not intent, so it must
	// not reach the worker.
	t.Setenv("CODE_SELECTION_STATE", "model=haiku;effort=low")

	var stderr bytes.Buffer
	code := run([]string{"analysis", "profile", "configure", "--worker", binary, "--worker-arg", "babel"},
		term.slave, term.slave, &stderr)
	displayed := term.collect(t)
	if code != exitOK {
		t.Fatalf("the ceremony exited %d\nstderr: %s\nterminal: %s", code, stderr.String(), displayed)
	}

	launch := launchRecord(t, record)
	for _, want := range []string{
		// The worker's own arguments come first, then the two flags Babel
		// owns: Code is put into its mode, then told where to answer.
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
	switch {
	case settings.Profile == nil:
		t.Fatalf("the confirmed profile was not stored:\n%s", settingsBytes(t))
	case settings.Profile.ID != "operator-chosen":
		t.Errorf("stored profile id = %q, want the one the worker wrote", settings.Profile.ID)
	case settings.Profile.Revision != 4:
		t.Errorf("stored revision = %d, want 4", settings.Profile.Revision)
	case settings.Profile.ConfiguredAt == "":
		t.Error("the stored reference does not record when it was configured")
	}
	if settings.Worker != binary || !slices.Equal(settings.WorkerArgs, []string{"babel"}) {
		t.Errorf("stored launch template = %q %v, want the one that was used", settings.Worker, settings.WorkerArgs)
	}
	// The ceremony carries a reference and nothing else, so nothing else may
	// appear in the document: an invented disclosure class or redaction
	// verdict would be a claim about what may leave this machine raw.
	if p := settings.Profile; p.RedactionRequired != nil || p.Disclosure != "" ||
		p.WorkerName != "" || p.ProtocolVersion != 0 || len(p.Capabilities) != 0 {
		t.Errorf("the ceremony stored metadata the worker never reported:\n%s", settingsBytes(t))
	}

	for _, want := range []string{"operator-chosen", "revision", "4"} {
		if !strings.Contains(displayed, want) {
			t.Errorf("the summary on the terminal does not show %q:\n%s", want, displayed)
		}
	}
	if strings.Contains(displayed, "not required") {
		t.Errorf("an unknown redaction requirement was displayed as a verdict:\n%s", displayed)
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
}

// TestProfileConfigureLeavesTheStoredProfileAlone holds the other half of the
// contract: every way the ceremony can end without a confirmed reference ends
// with the machine configured exactly as it was, said out loud, and a nonzero
// exit. A half-applied configuration is the failure mode that matters here —
// the operator would have no way to tell which profile the next run uses.
func TestProfileConfigureLeavesTheStoredProfileAlone(t *testing.T) {
	newFixture(t)
	confirmed, _ := ceremonyWorker(t, `{"profile":"operator-chosen","revision":4}`, 0)
	first := openTerminal(t)
	var stderr bytes.Buffer
	if code := run([]string{"analysis", "profile", "configure", "--worker", confirmed},
		first.slave, first.slave, &stderr); code != exitOK {
		t.Fatalf("storing the initial profile exited %d: %s", code, stderr.String())
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
		// case: the file on disk says one thing and the exit status says
		// another, and the exit status wins.
		{"the worker exits nonzero", `{"profile":"agent-minted","revision":9}`, 7, "exited 7"},
		{"the worker writes nothing", "", 0, "wrote no reference"},
		{"the worker writes something else", "not a document at all", 0, "not the JSON Babel reads"},
		{"the result names no profile", `{"revision":9}`, 0, "names no profile"},
		{"the revision is not positive", `{"profile":"agent-minted","revision":0}`, 0, "not a positive integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := openTerminal(t)
			binary, _ := ceremonyWorker(t, tc.payload, tc.code)
			var stderr bytes.Buffer
			code := run([]string{"analysis", "profile", "configure", "--worker", binary},
				term.slave, term.slave, &stderr)
			displayed := term.collect(t)
			if code != exitFailure {
				t.Fatalf("an abandoned ceremony exited %d, want %d\nstderr: %s", code, exitFailure, stderr.String())
			}
			for _, want := range []string{"configuration unchanged", tc.reason, "babel analysis profile show"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("the report does not mention %q:\n%s", want, stderr.String())
				}
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

// TestProfileConfigureRefusesAPreAnsweredProfile is the pass-through that had
// to go. --worker-arg puts Code into its worker mode; it was also a channel
// for handing Code a dial, which is how a profile gets minted with nobody
// deciding anything. Both the argument the operator types and the one a
// previous configuration stored are refused, and the worker is not launched:
// a machine whose stored launch template carries a dial keeps reproducing it
// otherwise, which is the recorded state of at least one machine.
func TestProfileConfigureRefusesAPreAnsweredProfile(t *testing.T) {
	newFixture(t)

	for _, arg := range []string{"--set", "--set=model=haiku", "-set-style", "--configure", "--result-file"} {
		t.Run("given "+arg, func(t *testing.T) {
			term := openTerminal(t)
			binary, record := ceremonyWorker(t, `{"profile":"dialled","revision":1}`, 0)
			var stderr bytes.Buffer
			code := run([]string{"analysis", "profile", "configure", "--worker", binary,
				"--worker-arg", "babel", "--worker-arg", arg}, term.slave, term.slave, &stderr)
			term.collect(t)
			if code != exitUsage {
				t.Fatalf("a pre-answered ceremony exited %d, want %d\nstderr: %s", code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "configuration override") {
				t.Errorf("the refusal does not say what was wrong:\n%s", stderr.String())
			}
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Error("the worker was launched with the override attached")
			}
		})
	}

	t.Run("stored by an earlier configuration", func(t *testing.T) {
		binary, record := ceremonyWorker(t, `{"profile":"dialled","revision":1}`, 0)
		if _, err := saveAnalysisSettings(analysisSettings{
			Worker:     binary,
			WorkerArgs: []string{"babel", "--set", "model=haiku"},
			Profile:    &profileRecord{ID: "agent-minted", Revision: 5, ConfiguredAt: "2026-08-30T00:00:00Z"},
		}); err != nil {
			t.Fatal(err)
		}
		before := settingsBytes(t)

		term := openTerminal(t)
		var stderr bytes.Buffer
		code := run([]string{"analysis", "profile", "configure"}, term.slave, term.slave, &stderr)
		term.collect(t)
		if code != exitUsage {
			t.Fatalf("a stored override exited %d, want %d\nstderr: %s", code, exitUsage, stderr.String())
		}
		for _, want := range []string{"stored worker arguments", "--worker"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("the refusal does not name the remedy %q:\n%s", want, stderr.String())
			}
		}
		if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
			t.Error("the worker was launched with the stored override attached")
		}
		if got := settingsBytes(t); got != before {
			t.Error("the refusal rewrote the settings document")
		}
	})
}
