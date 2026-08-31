package omp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// usageLog is a synthetic session whose recorded usage is small enough to
// add up by hand, which is the point: the expectations below are the
// arithmetic a reader can check against these lines rather than a golden
// value produced by the code under test.
//
// It holds every case the aggregate has to keep apart. Two turns are
// priced; a third records usage the harness never priced; a fourth records
// no usage at all and names a model that must therefore stay out of the
// model set, because the sets describe the turns whose tokens were
// counted. Tool calls are counted from assistant content and tool errors
// from the results, which are separate records and separately countable.
var usageLog = strings.Join([]string{
	`{"type":"title","v":1,"title":"Synthetic usage session","source":"auto"}`,
	`{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000e",` +
		`"timestamp":"2026-01-08T00:00:00.000Z","cwd":"/synthetic/workspace/usage"}`,
	`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}`,
	// Priced turn: two tool calls, a cache write, a quarter of a dollar.
	`{"type":"message","id":"a1","message":{"role":"assistant","model":"gpt-5.6-luna",` +
		`"provider":"openai-codex","content":[{"type":"text","text":"working"},` +
		`{"type":"toolCall","toolName":"read","args":{"path":"/x"}},` +
		`{"type":"toolCall","toolName":"bash","args":{"command":"ls"}}],` +
		`"usage":{"input":100,"output":10,"cacheRead":0,"cacheWrite":50,"totalTokens":160,` +
		`"cost":{"input":0.1,"output":0.05,"cacheRead":0,"cacheWrite":0.1,"total":0.25}}}}`,
	`{"type":"message","id":"r1","message":{"role":"toolResult","toolName":"read","isError":false,` +
		`"content":[{"type":"text","text":"ok"}]}}`,
	`{"type":"message","id":"r2","message":{"role":"toolResult","toolName":"bash","isError":true,` +
		`"content":[{"type":"text","text":"boom"}]}}`,
	// Priced turn on a different model and provider. Its reported total
	// exceeds input+output+cache because the provider counted reasoning
	// tokens separately, and the harness's own total is what is summed.
	`{"type":"message","id":"a2","message":{"role":"assistant","model":"claude-opus-5",` +
		`"provider":"anthropic","content":[{"type":"thinking","thinking":"hmm"},` +
		`{"type":"toolCall","toolName":"edit","args":{}}],` +
		`"usage":{"input":200,"output":20,"cacheRead":1000,"cacheWrite":0,"totalTokens":1220,` +
		`"reasoningTokens":5,"cost":{"input":0.2,"output":0.2,"cacheRead":0.1,"cacheWrite":0,"total":0.5}}}}`,
	`{"type":"message","id":"r3","message":{"role":"toolResult","toolName":"edit","isError":true,` +
		`"content":[{"type":"text","text":"boom"}]}}`,
	// A turn the harness measured but never priced: its tokens count, its
	// cost does not exist, and TurnsWithCost is what says so.
	`{"type":"message","id":"a3","message":{"role":"assistant","model":"gpt-5.6-luna",` +
		`"provider":"openai-codex","content":[{"type":"text","text":"done"}],` +
		`"usage":{"input":5,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":6}}}`,
	// A turn with no usage block at all. Its tool call still counts - the
	// call happened - while its model must not join the set describing the
	// tokens above.
	`{"type":"message","id":"a4","message":{"role":"assistant","model":"unmeasured-model",` +
		`"provider":"unmeasured-provider","content":[{"type":"toolCall","toolName":"read","args":{}}]}}`,
	// Records the aggregate must ignore rather than trip over: a role it
	// does not read, whose content OMP records as null, and a record that
	// is not a message at all.
	`{"type":"message","id":"b1","message":{"role":"bashExecution","content":null}}`,
	`{"type":"custom","customType":"tool_execution_start","data":{"toolName":"read"}}`,
}, "\n") + "\n"

// writeSessionLog plants one synthetic session log in its own root and
// returns the discovered source session, the way the torn-log and
// chunk-boundary tests do: the fixture is next to the expectations it
// justifies instead of in testdata, where a reader would have to open a
// second file to check the arithmetic.
func writeSessionLog(t *testing.T, project, stem, body string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "agent", "sessions")
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// The whole feature: a session's cost, tokens, turns, models and tool
// friction recomputed from the transcript's own usage blocks, exactly.
// Every number here is checked against usageLog above by hand, so a
// silently changed sum - a turn counted twice, a cache-read folded into
// input, a cost added for a turn the harness never priced - fails here.
func TestDescribeAggregatesRecordedUsage(t *testing.T) {
	t.Parallel()
	root := writeSessionLog(t, "-usage-project",
		"2026-01-08T00-00-00-000Z_00000000-0000-4000-8000-00000000000e", usageLog)
	desc := describe(t, session(t, discover(t, root), "-usage-project/"))

	u := desc.Usage
	if u == nil {
		t.Fatal("Usage = nil for a log whose assistant records all carry usage blocks")
	}

	// Turn accounting. Four assistant records, three measured, two priced:
	// the three counters are what let a reader see that the sums below
	// cover part of the session rather than all of it.
	if u.AssistantTurns != 4 {
		t.Errorf("AssistantTurns = %d, want 4", u.AssistantTurns)
	}
	if u.TurnsWithUsage != 3 {
		t.Errorf("TurnsWithUsage = %d, want 3: one assistant record carries no usage block", u.TurnsWithUsage)
	}
	if u.TurnsWithCost != 2 {
		t.Errorf("TurnsWithCost = %d, want 2: one measured turn was never priced", u.TurnsWithCost)
	}
	if u.UnreadableRecords != 0 {
		t.Errorf("UnreadableRecords = %d, want 0 for a well-formed log", u.UnreadableRecords)
	}

	// 0.25 + 0.50, and nothing for the turn with no cost object. Both
	// values are exact in binary floating point, so this is an equality
	// rather than a tolerance: an approximate check here would also pass
	// for a sum that included a third turn.
	if u.CostUSD != 0.75 {
		t.Errorf("CostUSD = %v, want 0.75 = 0.25 + 0.50", u.CostUSD)
	}

	// Token sums, per column, so a mis-mapped field cannot hide inside a
	// correct total.
	for _, c := range []struct {
		name string
		got  int64
		want int64
	}{
		{"InputTokens", u.InputTokens, 305},
		{"OutputTokens", u.OutputTokens, 31},
		{"CacheReadTokens", u.CacheReadTokens, 1000},
		{"CacheWriteTokens", u.CacheWriteTokens, 50},
		// 160 + 1220 + 6, the harness's own per-turn totals rather than the
		// sum of the four columns above: the middle turn reported 1220 for
		// 1220 counted tokens plus 5 reasoning tokens, and its number is
		// the authoritative one.
		{"TotalTokens", u.TotalTokens, 1386},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// The sets name what served the counted tokens. "unmeasured-model"
	// served a turn, but no tokens of it were counted, so listing it would
	// attribute the sums above to a model that contributed none of them.
	if want := []string{"claude-opus-5", "gpt-5.6-luna"}; !slices.Equal(u.Models, want) {
		t.Errorf("Models = %v, want %v sorted and deduplicated", u.Models, want)
	}
	if want := []string{"anthropic", "openai-codex"}; !slices.Equal(u.Providers, want) {
		t.Errorf("Providers = %v, want %v sorted and deduplicated", u.Providers, want)
	}

	// Tool friction: four calls across the assistant turns, two of the
	// three results marked as errors.
	if u.ToolCalls != 4 {
		t.Errorf("ToolCalls = %d, want 4", u.ToolCalls)
	}
	if u.ToolErrors != 2 {
		t.Errorf("ToolErrors = %d, want 2 of 3 tool results", u.ToolErrors)
	}

	// A measured session carries no completeness reason for usage: the
	// reason exists to explain an absence, and there is none.
	for _, r := range desc.Meta.Completeness {
		if r.Field == "usage" {
			t.Errorf("a measured session claims usage is absent: %q", r.Reason)
		}
	}

	// The same document travels in the adapter metadata, which is what
	// reaches the archive and what `sessions inspect` prints. It must be
	// the aggregate itself and not a second, drifting copy.
	var meta adapterMetadata
	if err := json.Unmarshal(desc.AdapterMetadata, &meta); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	if meta.Usage == nil {
		t.Fatal("adapter metadata carries no usage document")
	}
	inMeta, _ := json.Marshal(meta.Usage)
	described, _ := json.Marshal(u)
	if string(inMeta) != string(described) {
		t.Errorf("adapter metadata usage differs from Description.Usage:\n  meta: %s\n  desc: %s",
			inMeta, described)
	}
}

// The absence case, and the one that decides whether this feature is
// honest. A transcript written before OMP recorded per-turn usage produces
// no aggregate, and what it must not produce is an aggregate of zeros: a
// zero cost is a session that ran for free. The fixture session's one
// assistant record carries no usage block, which is exactly that shape.
func TestDescribeExplainsAbsentUsage(t *testing.T) {
	t.Parallel()
	desc := describe(t, session(t, discover(t, fixtureRoot(t)), "-synthetic-project/"))

	if desc.Usage != nil {
		t.Fatalf("Usage = %+v, want nil for a log with no usage blocks", *desc.Usage)
	}

	reason := ""
	for _, r := range desc.Meta.Completeness {
		if r.Field == "usage" {
			reason = r.Reason
		}
	}
	if reason == "" {
		t.Fatalf("absent usage carries no completeness reason (have %+v)", desc.Meta.Completeness)
	}
	if !strings.Contains(reason, "usage") {
		t.Errorf("completeness reason %q does not say what is missing", reason)
	}

	// The metadata document omits the field rather than encoding zeros, so
	// a consumer reading the archive meets the same absence.
	if strings.Contains(string(desc.AdapterMetadata), `"usage"`) {
		t.Errorf("adapter metadata encodes a usage document for an unmeasured session: %s",
			desc.AdapterMetadata)
	}
}

// Restic's snapshots are crash-consistent per file, so a log may be read
// mid-append or with a garbage line in it. The aggregate must sum what it
// can read and say how much it could not, because a partial sum presented
// as a whole one is the failure mode that matters: it under-reports spend
// with no sign that anything was skipped.
func TestUsageAggregateCountsUnreadableRecords(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000f",` +
			`"timestamp":"2026-01-09T00:00:00.000Z","cwd":"/synthetic/workspace/torn-usage"}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","model":"m","provider":"p",` +
			`"content":[{"type":"text","text":"one"}],` +
			`"usage":{"input":10,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":11,` +
			`"cost":{"total":0.5}}}}`,
		`not json at all`,
		// A second readable turn after the garbage line: the scan resumes
		// instead of stopping at the first record it cannot parse, which is
		// what makes the count above a hole rather than a truncation.
		`{"type":"message","id":"a2","message":{"role":"assistant","model":"m","provider":"p",` +
			`"content":[{"type":"text","text":"two"}],` +
			`"usage":{"input":20,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":22,` +
			`"cost":{"total":0.25}}}}`,
		// A torn tail: the record was being appended when the file was read.
		`{"type":"message","id":"a3","message":{"role":"assistant","usage":{"inp`,
	}, "\n")
	root := writeSessionLog(t, "-torn-usage",
		"2026-01-09T00-00-00-000Z_00000000-0000-4000-8000-00000000000f", body)
	desc := describe(t, session(t, discover(t, root), "-torn-usage/"))

	u := desc.Usage
	if u == nil {
		t.Fatal("Usage = nil although two records were readable")
	}
	if u.AssistantTurns != 2 || u.TurnsWithUsage != 2 {
		t.Errorf("AssistantTurns/TurnsWithUsage = %d/%d, want 2/2: the readable turns still count",
			u.AssistantTurns, u.TurnsWithUsage)
	}
	if u.CostUSD != 0.75 {
		t.Errorf("CostUSD = %v, want 0.75 from the two readable turns", u.CostUSD)
	}
	if u.TotalTokens != 33 {
		t.Errorf("TotalTokens = %d, want 33", u.TotalTokens)
	}
	if u.UnreadableRecords != 2 {
		t.Errorf("UnreadableRecords = %d, want 2: the garbage line and the torn tail",
			u.UnreadableRecords)
	}
}

// A cancelled describe must not report a partial sum. Every other read in
// a description degrades on failure, and this one cannot: a scan stopped
// halfway holds a number that looks exactly like a cheap session.
func TestUsageScanRefusesToReportAPartialSumOnCancellation(t *testing.T) {
	t.Parallel()
	root := writeSessionLog(t, "-cancelled-usage",
		"2026-01-10T00-00-00-000Z_00000000-0000-4000-8000-000000000010", usageLog)
	src := session(t, discover(t, root), "-cancelled-usage/")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := scanUsage(ctx, src.PrimaryPath); err == nil {
		t.Error("scanUsage returned a sum for a cancelled context")
	}
	if _, err := New().Describe(ctx, src); err == nil {
		t.Error("Describe returned a description for a cancelled context")
	}
}

// TestDescribeRealRootUsageSmoke aggregates the operator's real OMP tree
// read-only. It is opt-in and asserts only the invariants that must hold
// whatever the corpus contains; the numbers themselves are logged, which
// is what makes it useful as evidence that the extraction works on real
// transcripts rather than only on fixtures.
func TestDescribeRealRootUsageSmoke(t *testing.T) {
	if os.Getenv(realRootsEnv) == "" {
		t.Skipf("set %s to scan the real OMP root", realRootsEnv)
	}
	a := New()
	roots := a.DefaultRoots()
	if len(roots) == 0 {
		t.Skip("no home directory available")
	}
	if _, err := os.Stat(roots[0]); err != nil {
		t.Skip("default OMP root is absent")
	}
	sessions, err := a.Discover(context.Background(), roots)
	if err != nil {
		t.Fatalf("Discover on real root: %v", err)
	}

	var measured, unmeasured int
	var cost float64
	var tokens, turns, toolErrors int64
	for _, s := range sessions {
		desc, err := a.Describe(context.Background(), s)
		if err != nil {
			t.Fatalf("Describe(%s): %v", s.SourceID, err)
		}
		u := desc.Usage
		if u == nil {
			unmeasured++
			// An absence must always be explained, on the real corpus as
			// much as on a fixture.
			explained := false
			for _, r := range desc.Meta.Completeness {
				if r.Field == "usage" {
					explained = true
				}
			}
			if !explained {
				t.Errorf("%s reports no usage and no reason", s.SourceID)
			}
			continue
		}
		measured++
		if u.TurnsWithUsage > u.AssistantTurns || u.TurnsWithCost > u.TurnsWithUsage {
			t.Errorf("%s: turn counters are inconsistent: %+v", s.SourceID, *u)
		}
		if u.CostUSD < 0 || u.TotalTokens < 0 {
			t.Errorf("%s: negative measure: %+v", s.SourceID, *u)
		}
		cost += u.CostUSD
		tokens += u.TotalTokens
		turns += int64(u.AssistantTurns)
		toolErrors += int64(u.ToolErrors)
	}
	t.Logf("measured %d of %d real sessions (%d carry no usage blocks): $%.2f, %d tokens, %d assistant turns, %d tool errors",
		measured, len(sessions), unmeasured, cost, tokens, turns, toolErrors)
}
