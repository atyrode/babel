package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func migrationDocument(t *testing.T, analysisWorker, titlesWorker string) (string, []byte) {
	t.Helper()
	path, err := analysisPath()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(fmt.Sprintf(`{
  "schema": 1,
  "worker": %q,
  "worker_args": ["--account=research", "--quiet", "babel"],
  "profile": {"id":"chosen-analysis","revision":17,"configured_at":"original","metadata":{"model":"unchanged"},"future_profile":{"keep":true}},
  "titles": {"worker":%q,"worker_args":["--account=titles","babel"],"profile":"chosen-titles","revision":9,"configured_at":"also-original","future_title":[1,2]},
  "future_setting": {"nested":{"keep":"exact"}}
}
`, analysisWorker, titlesWorker))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func migrationRun(t *testing.T, wantCode int, args ...string) analysisMigrationResult {
	t.Helper()
	var out, stderr bytes.Buffer
	code := run(append([]string{"analysis", "migrate", "--json"}, args...), strings.NewReader(""), &out, &stderr)
	if code != wantCode {
		t.Fatalf("migration exit %d, want %d: %s; output: %s", code, wantCode, stderr.String(), out.String())
	}
	var result analysisMigrationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode migration result: %v; stderr: %s", err, stderr.String())
	}
	return result
}

func migrationUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("settings changed unexpectedly:\n%s", after)
	}
}

func TestAnalysisMigrationPreservesBothLaunchesAndExtensions(t *testing.T) {
	newFixture(t)
	analysisWrapper, analysisRecord := ceremonyWorker(t, "", 0)
	titlesWrapper, titlesRecord := ceremonyWorker(t, "", 0)
	path, before := migrationDocument(t, analysisWrapper, titlesWrapper)
	// Machine overrides must not silently replace stored account wrappers.
	t.Setenv("BABEL_ANALYSIS_WORKER", "/not/the/configured/worker")
	if got := migrationRun(t, exitFailure, "--check"); got != (analysisMigrationResult{Needed: true, Configured: 2}) {
		t.Fatalf("pending check: %+v", got)
	}
	migrationUnchanged(t, path, before)
	for _, record := range []string{analysisRecord, titlesRecord} {
		if _, err := os.Stat(record); !os.IsNotExist(err) {
			t.Fatalf("pending check launched a worker: %v", err)
		}
	}
	if got := migrationRun(t, exitOK); got != (analysisMigrationResult{Needed: true, Changed: true, Configured: 2}) {
		t.Fatalf("migration: %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(before, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatal(err)
	}
	want["worker_args"] = []any{"--account=research", "--quiet"}
	want["titles"].(map[string]any)["worker_args"] = []any{"--account=titles"}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("migration changed more than the terminal modes:\n%s", after)
	}
	for record, argv := range map[string]string{
		analysisRecord: "argv: --account=research --quiet engine --describe --profile chosen-analysis@17",
		titlesRecord:   "argv: --account=titles engine --describe --profile chosen-titles@9",
	} {
		if !strings.Contains(launchRecord(t, record), argv+"\n") {
			t.Fatalf("stored profile/account was not resolved offline: %s", launchRecord(t, record))
		}
	}
	if got := migrationRun(t, exitOK, "--check"); got != (analysisMigrationResult{Configured: 2}) {
		t.Fatalf("canonical check: %+v", got)
	}
	migrationUnchanged(t, path, after)
	if got := migrationRun(t, exitOK); got != (analysisMigrationResult{Configured: 2}) {
		t.Fatalf("repeat migration: %+v", got)
	}
	migrationUnchanged(t, path, after)
	// Canonical checks must still resolve refs, rather than report healthy
	// solely because the old argument is gone.
	if err := os.Remove(titlesWrapper); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"analysis", "migrate", "--check", "--json"}, nil, &out, &stderr); code != exitFailure {
		t.Fatalf("unresolvable canonical profile exited %d", code)
	}
	migrationUnchanged(t, path, after)
}

func TestAnalysisMigrationFailureLeavesBothBlocksUntouched(t *testing.T) {
	newFixture(t)
	good, _ := ceremonyWorker(t, "", 0)
	bad, _ := ceremonyStub(t, "", 0, 3)
	path, before := migrationDocument(t, good, bad)
	var out, stderr bytes.Buffer
	if code := run([]string{"analysis", "migrate", "--json"}, nil, &out, &stderr); code != exitFailure {
		t.Fatalf("failed title resolution exited %d: %s", code, out.String())
	}
	migrationUnchanged(t, path, before)
}

func TestAnalysisMigrationRejectsAmbiguousArgumentsBeforeLaunch(t *testing.T) {
	for _, argv := range []string{
		`["babel","--quiet"]`,
		`["--account","babel","babel"]`,
		`["engine","babel"]`,
		`["--profile=other@1","babel"]`,
	} {
		t.Run(argv, func(t *testing.T) {
			newFixture(t)
			binary, record := ceremonyWorker(t, "", 0)
			path, data := migrationDocument(t, binary, binary)
			data = bytes.Replace(data, []byte(`["--account=research", "--quiet", "babel"]`), []byte(argv), 1)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, flags := range [][]string{nil, {"--check"}} {
				var out, stderr bytes.Buffer
				if code := run(append([]string{"analysis", "migrate", "--json"}, flags...), nil, &out, &stderr); code == exitOK {
					t.Fatal("ambiguous arguments were accepted")
				}
				migrationUnchanged(t, path, data)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatalf("ambiguous launch reached the worker: %v", err)
			}
		})
	}
}

func TestAnalysisMigrationUnconfiguredDoesNotCreateSettings(t *testing.T) {
	newFixture(t)
	path, err := analysisPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--check"}} {
		if got := migrationRun(t, exitOK, flags...); got != (analysisMigrationResult{}) {
			t.Fatalf("unconfigured migration: %+v", got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unconfigured migration created settings: %v", err)
		}
	}
	// A pre-existing settings file with no profile blocks is equally
	// unconfigured, even if somebody recorded a worker location in it.
	data := []byte(`{"schema":1,"worker":"/missing-wrapper","worker_args":["babel"],"future":{"keep":true}}`)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--check"}} {
		if got := migrationRun(t, exitOK, flags...); got != (analysisMigrationResult{}) {
			t.Fatalf("unconfigured existing settings: %+v", got)
		}
		migrationUnchanged(t, path, data)
	}
}
