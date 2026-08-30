package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
)

// Synthetic Codex records. No real transcript content, path, or identifier
// appears in them; the wrapper shapes are reproduced because they are the
// format, not the operator's data.
const (
	// The five injected-context shapes observed in real logs. Each is a
	// `response_item` on the model-input channel, which is where Codex puts
	// them and where a naive "first user message" rule finds them.
	injectedPermissions = `{"type":"response_item","payload":{"type":"message","role":"developer",` +
		`"content":[{"type":"input_text","text":"<permissions instructions>\nsynthetic sandbox policy\n</permissions instructions>"}]}}`
	injectedEnvironment = `{"type":"response_item","payload":{"type":"message","role":"user",` +
		`"content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/synthetic/workspace</cwd>\n</environment_context>"}]}}`
	injectedPlugins = `{"type":"response_item","payload":{"type":"message","role":"user",` +
		`"content":[{"type":"input_text","text":"<recommended_plugins>\nsynthetic plugin list\n</recommended_plugins>"}]}}`
	injectedAgentsMD = `{"type":"response_item","payload":{"type":"message","role":"user",` +
		`"content":[{"type":"input_text","text":"# AGENTS.md instructions for /synthetic/workspace\n\n<INSTRUCTIONS>\nsynthetic repository rules\n</INSTRUCTIONS>"}]}}`
	injectedMultiAgent = `{"type":"response_item","payload":{"type":"message","role":"developer",` +
		`"content":[{"type":"input_text","text":"<multi_agent_mode>synthetic delegation policy</multi_agent_mode>"}]}}`

	turnContext = `{"type":"turn_context","payload":{"cwd":"/synthetic/workspace","model":"synthetic-model"}}`
)

// metaRecord builds a session_meta record with the given source union.
func metaRecord(source string) string {
	return `{"timestamp":"2026-01-02T03:04:05.000Z","type":"session_meta","payload":{` +
		`"id":"aaaaaaaa-0000-4000-8000-00000000000f","timestamp":"2026-01-02T03:04:05.000Z",` +
		`"cwd":"/synthetic/workspace","source":` + source + `}}`
}

// deliveredRequest builds an `event_msg` user_message: the front end's record
// of a turn that was actually delivered.
func deliveredRequest(text string) string {
	return `{"timestamp":"2026-01-02T03:04:06.000Z","type":"event_msg","payload":{` +
		`"type":"user_message","message":` + jsonString(text) + `}}`
}

// modelInputRequest builds a `response_item` user message: the model-input
// channel, which is also where injected context lives.
func modelInputRequest(text string) string {
	return `{"timestamp":"2026-01-02T03:04:06.000Z","type":"response_item","payload":{` +
		`"type":"message","role":"user","content":[{"type":"input_text","text":` + jsonString(text) + `}]}}`
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// describeLog writes one synthetic rollout into a throwaway Codex root and
// describes it through the public adapter, so every assertion below is about
// what a caller actually receives.
func describeLog(t *testing.T, records ...string) *adapter.Description {
	t.Helper()
	root := filepath.Join(t.TempDir(), "codex")
	dir := filepath.Join(root, "sessions", "2026", "01", "02")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	rel := "sessions/2026/01/02/rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-00000000000f.jsonl"
	primary := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.WriteFile(primary, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	desc, err := New().Describe(context.Background(), adapter.SourceSession{
		Harness:     HarnessName,
		SourceID:    rel,
		PrimaryPath: primary,
	})
	if err != nil {
		t.Fatalf("describe fixture: %v", err)
	}
	return desc
}

// titleReason returns the completeness reason recorded for the title field.
func titleReason(t *testing.T, desc *adapter.Description) string {
	t.Helper()
	for _, r := range desc.Meta.Completeness {
		if r.Field == "title" {
			return r.Reason
		}
	}
	t.Fatalf("no completeness reason for the absent title: %+v", desc.Meta.Completeness)
	return ""
}

// requireNoTitle asserts the honest-absence contract: no title, no provenance,
// and a reason a reader can act on.
func requireNoTitle(t *testing.T, desc *adapter.Description, wantReasonSubstring string) {
	t.Helper()
	if desc.Meta.Title != nil {
		t.Fatalf("Title = %q, want none", *desc.Meta.Title)
	}
	if desc.Meta.TitleProvenance != "" {
		t.Errorf("TitleProvenance = %q with no title; a provenance names the origin of nothing",
			desc.Meta.TitleProvenance)
	}
	reason := titleReason(t, desc)
	if !strings.Contains(reason, wantReasonSubstring) {
		t.Errorf("reason = %q, want it to mention %q", reason, wantReasonSubstring)
	}
}

// requireTitle asserts a derived title and the pairing that makes it readable
// as Babel's own rather than Codex's.
func requireTitle(t *testing.T, desc *adapter.Description, want string) {
	t.Helper()
	if desc.Meta.Title == nil {
		t.Fatalf("Title = nil, want %q (reason given: %q)", want, titleReason(t, desc))
	}
	if *desc.Meta.Title != want {
		t.Errorf("Title = %q, want %q", *desc.Meta.Title, want)
	}
	if desc.Meta.TitleProvenance != adapter.TitleDerived {
		t.Errorf("TitleProvenance = %q, want %q: Codex records no title, so a title here is "+
			"always Babel's derivation and must never read as the harness's own record",
			desc.Meta.TitleProvenance, adapter.TitleDerived)
	}
	for _, r := range desc.Meta.Completeness {
		if r.Field == "title" {
			t.Errorf("a present title also carries an absence reason: %q", r.Reason)
		}
	}
}

// TestInjectedPreambleNeverBecomesTitle is the load-bearing test of the
// derivation, and the one whose fixture proves the rule is not vacuous.
//
// The log contains every injected shape observed in the operator's real corpus
// and no caller request at all. A rule that took the first user-role message
// would title this session "<environment_context> <cwd>/synthetic/workspace…";
// a rule that pattern-matched three known strings would still be defeated by
// the fourth. The only correct answer is no title and a reason.
//
// Non-vacuity: deleting the `injectedBlock` guard from titleFromRequest makes
// this test fail with
//
//	Title = "<recommended_plugins> synthetic plugin list </recommended_plugins>", want none
//
// which is exactly the failure mode the guard exists to prevent.
func TestInjectedPreambleNeverBecomesTitle(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"agent_role":"explorer"}}}`),
		injectedPermissions,
		injectedMultiAgent,
		injectedEnvironment,
		injectedAgentsMD,
		injectedPlugins,
		turnContext,
	)
	requireNoTitle(t, desc, "no delivered request record exposed titleable text")
}

// TestInjectedBlockOnTheEventChannelIsStillRefused exercises the second guard
// site. The shape test runs twice on purpose and the two are not redundant:
// collectFallbackRequest uses it to choose *which* of up to eight model-input
// records to retain, so it must skip injected ones to reach the real request
// behind them; titleFromRequest uses it on whichever candidate arrives,
// including from the event channel, which has no scan-time filter at all
// because no injected block has ever appeared there in 640 real logs.
//
// That "has ever" is the reason for this test. The channel rule rests on an
// observation about Codex's current behaviour, not a guarantee it publishes,
// and the day it delivers a wrapped envelope as a `user_message` the honest
// outcome is no title rather than a preamble promoted to one.
//
// Non-vacuity: deleting the injectedBlock call from titleFromRequest makes this
// test fail with
//
//	Title = "<environment_context> <cwd>/synthetic/workspace</cwd> </environment_context>", want none
func TestInjectedBlockOnTheEventChannelIsStillRefused(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		turnContext,
		deliveredRequest("<environment_context>\n  <cwd>/synthetic/workspace</cwd>\n</environment_context>"),
	)
	requireNoTitle(t, desc, "no delivered request record exposed titleable text")
}

// TestDeliveredRequestBecomesTitle is the same log with one genuine turn added
// on the event channel. The injected records are unchanged and still come
// first, so what this asserts is that the channel rule finds the request past
// them rather than that the fixture happened to be clean.
func TestDeliveredRequestBecomesTitle(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		injectedPermissions,
		injectedEnvironment,
		injectedAgentsMD,
		turnContext,
		modelInputRequest("could you check whether the synthetic importer still retries?"),
		deliveredRequest("could you check whether the synthetic importer still retries?"),
	)
	requireTitle(t, desc, "could you check whether the synthetic importer still retries?")
}

// TestModelInputChannelIsTheFallback covers a log that emits no delivered-turn
// event — three of the operator's 640 do. The request must still be found on
// the model-input channel, and the injected records before it must still be
// skipped, which is where the shape guard rather than the channel does the work.
func TestModelInputChannelIsTheFallback(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		injectedPermissions,
		injectedEnvironment,
		injectedPlugins,
		turnContext,
		modelInputRequest("restart the synthetic queue worker and report what it was stuck on"),
	)
	requireTitle(t, desc, "restart the synthetic queue worker and report what it was stuck on")
}

// TestBuiltInRoleThreadGetsNoTitle covers 288 of the operator's 640 rollouts:
// threads Codex opened for its own `guardian` approval assessor, whose first
// delivered turn is a fixed harness template. The template is a real delivered
// `user_message`, so the channel rule alone would happily title all 288 of them
// identically; the source union is what says there is no caller request here.
func TestBuiltInRoleThreadGetsNoTitle(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"other":"guardian"}}`),
		injectedPermissions,
		injectedEnvironment,
		turnContext,
		deliveredRequest("The following is the Codex agent history whose request action you are assessing."),
	)
	requireNoTitle(t, desc, `built-in "guardian" role`)
}

// TestSpawnedThreadIsTitledFromItsAgentPath covers the largest group of the
// corpus, and the one where the transcript is actively wrong: 246 of 312
// spawned threads open by replaying their parent's conversation, so the
// delivered request belongs to a different session. `agent_path` is the only
// value Codex records about this thread's own job.
//
// The fixture makes them disagree deliberately. If the rule ever prefers the
// transcript, this test says so.
func TestSpawnedThreadIsTitledFromItsAgentPath(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"parent_thread_id":"aaaaaaaa-0000-4000-8000-00000000000e","agent_path":"/root/synthetic_node_version_audit"}}}`),
		injectedPermissions,
		turnContext,
		deliveredRequest("a replayed parent request that is about a different session entirely"),
	)
	requireTitle(t, desc, "Synthetic node version audit")
}

// TestNestedAgentPathUsesItsLeaf: Codex nests spawned paths, and the leaf names
// this thread while the ancestors name the threads that delegated it — which
// are already reachable by parent id.
func TestNestedAgentPathUsesItsLeaf(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"agent_path":"/root/synthetic_audit/research-clan-alts/"}}}`),
		turnContext,
	)
	requireTitle(t, desc, "Research clan alts")
}

// TestRoledSpawnWithoutAgentPathUsesItsBrief covers the 12 corpus spawns that
// carry an `agent_role` but no path. Codex declaring a role is it declaring the
// thread was opened with a brief of its own, and in all 12 the delivered
// request was that brief rather than a replay.
func TestRoledSpawnWithoutAgentPathUsesItsBrief(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"agent_role":"explorer","agent_path":null}}}`),
		injectedEnvironment,
		turnContext,
		deliveredRequest("Research task: map the synthetic renderer's asset pipeline, read only."),
	)
	requireTitle(t, desc, "Research task: map the synthetic renderer's asset pipeline, read only.")
}

// TestPathlessRolelessSpawnGetsNoTitle is the conservative case: nothing in the
// log is known to be about this thread rather than its parent, so the honest
// answer is no title. One corpus session is in this state, and its delivered
// request is verifiably its parent's.
func TestPathlessRolelessSpawnGetsNoTitle(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"parent_thread_id":"aaaaaaaa-0000-4000-8000-00000000000e"}}}`),
		turnContext,
		deliveredRequest("a replayed parent request that is about a different session entirely"),
	)
	requireNoTitle(t, desc, "transcript may replay its parent")
}

// TestComposedEnvelopeIsReadFromItsLastSection covers the shape that produced
// the corpus's worst titles. When files are attached, Codex Desktop delivers
// the turn as a markdown document listing the attachments, with the operator's
// own text in the section after them; titling from the front yields a run of
// clipboard filenames.
func TestComposedEnvelopeIsReadFromItsLastSection(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		turnContext,
		deliveredRequest("\n# Files mentioned by the user:\n\n"+
			"## codex-clipboard-0000.png: /synthetic/.codex/attachments/aaaa/codex-clipboard-0000.png\n\n"+
			"## My request for Codex:\nthe synthetic chart legend overlaps the axis, can you fix it?\n"),
	)
	requireTitle(t, desc, "the synthetic chart legend overlaps the axis, can you fix it?")
}

// TestComposedEnvelopeWithNoRequestGetsNoTitle: two of the four corpus
// envelopes have an empty last section, because the request itself was the
// attachment. The attachment's bytes are archived; its text is not this
// adapter's to read, so the session honestly has no title.
func TestComposedEnvelopeWithNoRequestGetsNoTitle(t *testing.T) {
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		turnContext,
		deliveredRequest("\n# Files mentioned by the user:\n\n"+
			"## /synthetic/.codex/attachments/aaaa/pasted-text.txt\n\n"+
			"The attached pasted text file(s) contain the user's request.\n\n"+
			"## My request for Codex:\n\n"),
	)
	requireNoTitle(t, desc, "no delivered request record exposed titleable text")
}

// TestLongRequestIsBoundedAndMarkedAsCut: a title is a display value in a
// terminal column and in a plaintext catalog row, so it is bounded here rather
// than by every reader — and the cut is visible, so a reader is not left
// thinking the operator stopped mid-sentence.
func TestLongRequestIsBoundedAndMarkedAsCut(t *testing.T) {
	long := "please " + strings.Repeat("investigate the synthetic subsystem ", 20)
	desc := describeLog(t,
		metaRecord(`"vscode"`),
		turnContext,
		deliveredRequest(long),
	)
	if desc.Meta.Title == nil {
		t.Fatal("a long request yielded no title")
	}
	got := *desc.Meta.Title
	if runeLen(got) > maxTitleRunes+1 {
		t.Errorf("title is %d runes, want at most %d plus the ellipsis", runeLen(got), maxTitleRunes)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Errorf("truncated title %q does not show that it was cut", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("title %q is not a single line", got)
	}
}

// TestTitleDerivationIsRecordedInAdapterMetadata: which rule produced a title
// is a structural fact a reader can use to judge it, and recording the rule
// name rather than the text keeps that document's promise to hold no
// transcript content.
func TestTitleDerivationIsRecordedInAdapterMetadata(t *testing.T) {
	spawn := describeLog(t,
		metaRecord(`{"subagent":{"thread_spawn":{"agent_path":"/root/synthetic_probe"}}}`),
		turnContext,
	)
	if md := metadataOf(t, spawn); md.TitleDerivation != string(basisAgentPath) {
		t.Errorf("TitleDerivation = %q, want %q", md.TitleDerivation, basisAgentPath)
	}
	interactive := describeLog(t,
		metaRecord(`"vscode"`),
		turnContext,
		deliveredRequest("a synthetic interactive request"),
	)
	md := metadataOf(t, interactive)
	if md.TitleDerivation != string(basisRequest) {
		t.Errorf("TitleDerivation = %q, want %q", md.TitleDerivation, basisRequest)
	}
	if strings.Contains(md.TitleDerivation, "synthetic interactive request") {
		t.Error("adapter metadata leaked transcript text into the derivation field")
	}

	untitled := describeLog(t, metaRecord(`{"subagent":{"other":"guardian"}}`), turnContext)
	if md := metadataOf(t, untitled); md.TitleDerivation != "" {
		t.Errorf("TitleDerivation = %q for an untitled session, want empty", md.TitleDerivation)
	}
}

// TestHostStateHasNoTitle: `history.jsonl` is a per-host log rather than a
// conversation, so there is no request to name it after and the reason says so
// instead of reusing the per-session wording.
func TestHostStateHasNoTitle(t *testing.T) {
	root := fixtureRoot(t)
	desc := describeOf(t, root, StateSourceID)
	requireNoTitle(t, desc, "not a session with a request to name it")
}

// TestInjectedContextIsExportedForCallersThatReadCodexMessages pins the
// exported predicate against the same five shapes, because the inferred-title
// path uses it to decide what is worth paying a provider to summarize.
func TestInjectedContextIsExportedForCallersThatReadCodexMessages(t *testing.T) {
	injected := []string{
		"<permissions instructions>\nsynthetic sandbox policy",
		"<environment_context>\n  <cwd>/synthetic</cwd>",
		"<recommended_plugins>\nsynthetic list",
		"# AGENTS.md instructions for /synthetic\n\n<INSTRUCTIONS>\nrules",
		"<multi_agent_mode>synthetic policy</multi_agent_mode>",
		"   ",
	}
	for _, text := range injected {
		if !InjectedContext(text) {
			t.Errorf("InjectedContext(%q) = false, want true", firstLineOf(text))
		}
	}
	requests := []string{
		"could you check the synthetic importer?",
		"# Files mentioned by the user:\n\n## My request for Codex:\nfix the chart",
		"a < b, and b < c",
		"2 < 3 is true",
	}
	for _, text := range requests {
		if InjectedContext(text) {
			t.Errorf("InjectedContext(%q) = true, want false", firstLineOf(text))
		}
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
