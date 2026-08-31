package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// Machine-readable conductor shapes, mirrored here for the same consumer-side
// reason as the rest of this suite: decoding with DisallowUnknownFields makes
// these tests a contract test of the documents the CLI emits.

type conductorConfigDoc struct {
	Currency           string  `json:"currency"`
	PerCycle           float64 `json:"per_cycle"`
	PerDay             float64 `json:"per_day"`
	Floor              int     `json:"serendipity_floor"`
	IntervalSeconds    int     `json:"interval_seconds"`
	SliceSessions      int     `json:"slice_sessions"`
	BabelImprovesBabel bool    `json:"babel_improves_babel"`
	BabelTunesItself   bool    `json:"babel_tunes_itself"`
	ConfiguredAt       string  `json:"configured_at"`
	Path               string  `json:"path"`
}

type conductorCycleDoc struct {
	Seq           int      `json:"seq"`
	Outcome       string   `json:"outcome"`
	Reason        string   `json:"reason"`
	Rung          string   `json:"rung"`
	AuthorityKind string   `json:"authority_kind"`
	AuthorityRef  string   `json:"authority_ref"`
	RunID         string   `json:"run_id"`
	Invitation    string   `json:"invitation"`
	Resumed       bool     `json:"resumed"`
	Sessions      int      `json:"sessions"`
	Recipes       []string `json:"recipes"`
	Note          string   `json:"note"`
	PreparationID string   `json:"preparation_id"`
	ReceiptID     string   `json:"receipt_id"`
	Cost          float64  `json:"cost"`
	Currency      string   `json:"currency"`
	Failures      int      `json:"failures"`
	StartedAt     string   `json:"started_at"`
	FinishedAt    string   `json:"finished_at"`
}

type conductorRunDoc struct {
	Cycles []conductorCycleDoc `json:"cycles"`
	Parked string              `json:"parked"`
}

type conductorRungDoc struct {
	Name        string `json:"name"`
	Waiting     int    `json:"waiting"`
	Implemented bool   `json:"implemented"`
	Note        string `json:"note"`
}

type conductorDutyDoc struct {
	Name      string `json:"name"`
	Recipe    string `json:"recipe"`
	Dimension string `json:"dimension"`
	Enabled   bool   `json:"enabled"`
	Due       bool   `json:"due"`
	LastDrawn string `json:"last_drawn"`
	Note      string `json:"note"`
}

type conductorSpendDoc struct {
	Currency   string  `json:"currency"`
	Spent      float64 `json:"spent"`
	PerDay     float64 `json:"per_day"`
	Remaining  float64 `json:"remaining"`
	PerCycle   float64 `json:"per_cycle"`
	Runs       int     `json:"runs"`
	Unpriced   int     `json:"unpriced"`
	Journalled float64 `json:"journalled"`
}

type conductorStatusDoc struct {
	Configured bool                `json:"configured"`
	State      string              `json:"state"`
	Config     *conductorConfigDoc `json:"config"`
	Current    *conductorCycleDoc  `json:"current"`
	Rungs      []conductorRungDoc  `json:"rungs"`
	Duties     []conductorDutyDoc  `json:"duties"`
	Spend      *conductorSpendDoc  `json:"spend"`
	Cycles     []conductorCycleDoc `json:"cycles"`
	Journal    string              `json:"journal"`
	// Fleet is #118's presence block: what every machine says it is running.
	// On these local-mode deployments it is always empty with the note saying
	// why, and its shape is asserted where the presence tests own it; here it
	// only has to be a declared part of the document.
	Fleet     []map[string]any `json:"fleet"`
	FleetNote string           `json:"fleet_note,omitempty"`
}

// The conductor, driven end to end through the shipped executable against the
// synthetic worker: configure the ceilings, run one cycle on the operator's
// invitation, run one on the serendipity floor, and read the loop back.
//
// What this scenario is really checking is that the loop adds scheduling and
// nothing else. Every cycle has to land as an ordinary run with an ordinary
// receipt, the receipt has to say by whose authority it happened, and the
// status view has to agree with the durable records rather than with its own
// bookkeeping. No model is involved anywhere: the worker is the protocol
// counterpart every other Phase B test uses.
func TestConductorCyclesAreAttributableRuns(t *testing.T) {
	ctx := context.Background()
	p := newPhaseB(t)
	worker := fakeWorker(t)
	payload := writeCandidatePayload(t, p)
	workerArgs := []string{
		"--worker", worker,
		"--worker-arg", "-result-payload-selector", "--worker-arg", explore.ParamStage,
		"--worker-arg", "-result-payload", "--worker-arg", string(explore.StageExplore) + "=" + payload,
	}

	// An unconfigured conductor refuses to run, and says what to do about it.
	// Autonomy is budget-bounded, so this is the design rather than a fault.
	_, stderr, code := p.exec(t, append([]string{"conductor", "run", "--once"}, workerArgs...)...)
	if code != exitFailure {
		t.Fatalf("an unconfigured conductor exited %d, want a refusal", code)
	}
	if !strings.Contains(stderr, "budget ceilings") || !strings.Contains(stderr, "conductor configure") {
		t.Fatalf("the refusal does not name the remedy:\n%s", stderr)
	}

	// A status view of a machine where the loop has never run is still a
	// status view: unconfigured, idle, and honest about the absent rung.
	cold := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	if cold.Configured || cold.State != "idle" || cold.Spend != nil || len(cold.Cycles) != 0 {
		t.Fatalf("cold status = %+v", cold)
	}
	assertLadderShape(t, cold.Rungs)

	// Configure the ceilings. Both are mandatory; nothing here names a model.
	configured := execJSON[conductorConfigDoc](t, p,
		"conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00",
		"--interval", "0s", "--json")
	if configured.PerCycle != 0.50 || configured.PerDay != 5.00 || configured.Currency != "USD" {
		t.Fatalf("configured ceilings = %+v", configured)
	}
	if configured.Floor != 4 {
		t.Errorf("serendipity floor = %d, want the default of one in four", configured.Floor)
	}
	if configured.ConfiguredAt == "" || !strings.HasSuffix(configured.Path, "conductor.json") {
		t.Errorf("configuration document = %+v", configured)
	}

	// A cycle inherits the stored profile: the conductor never configures one,
	// and there is no --profile flag for it to be handed one on. The ceremony
	// that mints a profile needs a terminal and a Code build, so the stored
	// document is planted here exactly as that ceremony would leave it.
	plantProfile(t, p, "synthetic-profile", 1)

	// One manual run first, both to give the frontier something an operator can
	// point at and to record what a typed command's authority looks like.
	prepared := execJSON[prepareDoc](t, p, "prepare", "--harness", "omp", "--json")
	manual := execJSON[exploreDoc](t, p, append([]string{
		"explore", "--preparation", prepared.PreparationID,
		"--profile", exploreProfile, "--json",
	}, workerArgs...)...)
	if len(manual.Hypotheses) == 0 {
		t.Fatalf("the manual run produced no candidate to invite: %+v", manual)
	}
	assertReceiptAuthority(t, p, manual.ReceiptID, "operator", "command:explore")

	// The operator invites the loop to process that candidate further (#87),
	// which is rung one of the ladder.
	invited := execJSON[inviteDoc](t, p,
		"invite", manual.Hypotheses[0], "--operator", explorationOperator, "--json")
	invitationID := invited.Invitation.ID
	if invitationID == "" {
		t.Fatalf("invite produced no invitation: %+v", invited)
	}

	// The queue depth is visible before the cycle that drains it.
	queued := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	if depth := rungDepth(t, queued.Rungs, "invitation"); depth != 1 {
		t.Fatalf("invitation rung depth = %d, want the one invitation just left", depth)
	}
	if queued.Spend == nil || queued.Spend.PerDay != 5.00 {
		t.Fatalf("configured status reports spend %+v", queued.Spend)
	}

	// Cycle one: the operator's invitation, consumed once and recorded.
	first := execJSON[conductorRunDoc](t, p, append([]string{"conductor", "run", "--once", "--json"}, workerArgs...)...)
	if len(first.Cycles) != 1 {
		t.Fatalf("--once ran %d cycles: %+v", len(first.Cycles), first.Cycles)
	}
	cycle := first.Cycles[0]
	if cycle.Outcome != "ran" || cycle.Rung != "invitation" {
		t.Fatalf("first cycle = %+v, want a completed invitation cycle", cycle)
	}
	if cycle.Invitation != invitationID {
		t.Errorf("cycle consumed %q, want %q", cycle.Invitation, invitationID)
	}
	if cycle.AuthorityKind != "operator" || cycle.AuthorityRef != "invitation:"+invitationID {
		t.Errorf("cycle authority = %s/%s", cycle.AuthorityKind, cycle.AuthorityRef)
	}
	if cycle.ReceiptID == "" || cycle.PreparationID == "" {
		t.Fatalf("an invitation cycle produced no ordinary run: %+v", cycle)
	}
	// The receipt is the durable half of the claim, and it carries the same why.
	assertReceiptAuthority(t, p, cycle.ReceiptID, "operator", "invitation:"+invitationID)

	// The invitation is spent, so rung one is empty again.
	if depth := rungDepth(t, execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json").Rungs,
		"invitation"); depth != 0 {
		t.Errorf("invitation rung depth after the cycle = %d, want 0", depth)
	}

	// Cycle two: nothing is waiting, so the loop falls to its serendipity
	// floor and declares the draw rather than borrowing a reason.
	second := execJSON[conductorRunDoc](t, p, append([]string{"conductor", "run", "--once", "--json"}, workerArgs...)...)
	if len(second.Cycles) != 1 {
		t.Fatalf("--once ran %d cycles: %+v", len(second.Cycles), second.Cycles)
	}
	draw := second.Cycles[0]
	if draw.Outcome != "ran" || draw.Rung != "serendipity" {
		t.Fatalf("second cycle = %+v, want a completed serendipity cycle", draw)
	}
	if draw.AuthorityKind != "serendipity" || !strings.HasPrefix(draw.AuthorityRef, "draw:") {
		t.Errorf("draw authority = %s/%s", draw.AuthorityKind, draw.AuthorityRef)
	}
	if draw.Sessions == 0 || draw.Sessions > 3 {
		t.Errorf("the draw took %d sessions, want a bounded slice", draw.Sessions)
	}
	if len(draw.Recipes) != 1 {
		t.Errorf("the draw ran %v, want exactly one recipe", draw.Recipes)
	}
	if draw.RunID == cycle.RunID {
		t.Error("two cycles shared one run identity")
	}
	assertReceiptAuthority(t, p, draw.ReceiptID, "serendipity", draw.AuthorityRef)

	// The status view agrees with the durable records: two cycles, the loop
	// idle between them, and today's spend read from the receipts rather than
	// from the loop's own arithmetic.
	final := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json", "--cycles", "5")
	if !final.Configured || final.State != "idle" || final.Current != nil {
		t.Fatalf("final status = %+v", final)
	}
	if len(final.Cycles) != 2 {
		t.Fatalf("status reports %d cycles, want 2: %+v", len(final.Cycles), final.Cycles)
	}
	// Newest first, and each cycle still says by whose authority it ran.
	if final.Cycles[0].Seq != 2 || final.Cycles[1].Seq != 1 {
		t.Errorf("cycles are not newest-first: %+v", final.Cycles)
	}
	if final.Cycles[0].AuthorityKind != "serendipity" || final.Cycles[1].AuthorityKind != "operator" {
		t.Errorf("recorded authorities = %q, %q",
			final.Cycles[0].AuthorityKind, final.Cycles[1].AuthorityKind)
	}
	if final.Spend == nil {
		t.Fatal("a configured conductor reported no spend")
	}
	// The synthetic worker quotes its cost in a currency the ceilings are not
	// in, so every run today is unpriced rather than free — which is exactly
	// what the status view has to say instead of showing a reassuring zero.
	if final.Spend.Unpriced < 3 {
		t.Errorf("spend = %+v, want the unpriceable runs counted", final.Spend)
	}
	if final.Spend.Spent != 0 || final.Spend.Remaining != 5.00 {
		t.Errorf("spend = %+v, want nothing priced against the ceiling", final.Spend)
	}
	if !strings.HasSuffix(final.Journal, "conductor.json") {
		t.Errorf("journal path = %q", final.Journal)
	}

	// And the frontier grew from the cycles rather than from nothing: the loop
	// mints no output kind of its own, so what a cycle produces is what a run
	// produces.
	front, err := frontier.Open(p.durableDir())
	if err != nil {
		t.Fatalf("open the frontier: %v", err)
	}
	defer front.Close()
	unexplored, err := front.Unexplored(ctx, 0)
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}
	if len(unexplored) == 0 {
		t.Error("three runs left no candidates on the frontier")
	}

	// The terminal rendering names the authority too: the loop's legibility is
	// not a --json-only feature.
	stdout, _ := p.okExec(t, "conductor", "status")
	for _, want := range []string{"serendipity", "invitation", "ladder", "duties"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("conductor status does not mention %q:\n%s", want, stdout)
		}
	}
}

// A conductor whose day is already spent parks with a reason instead of running,
// and parking is not a failure exit: the ceilings are working.
func TestConductorParksOnTheDailyCeiling(t *testing.T) {
	p := newPhaseB(t)
	worker := fakeWorker(t)
	payload := writeCandidatePayload(t, p)
	workerArgs := []string{
		"--worker", worker,
		"--worker-arg", "-result-payload-selector", "--worker-arg", explore.ParamStage,
		"--worker-arg", "-result-payload", "--worker-arg", string(explore.StageExplore) + "=" + payload,
	}
	plantProfile(t, p, "synthetic-profile", 1)

	// A day ceiling equal to one cycle's ceiling: the first cycle already
	// leaves no room, because a cycle may cost the whole day's budget.
	execJSON[conductorConfigDoc](t, p, "conductor", "configure",
		"--per-cycle", "1.00", "--per-day", "1.00", "--interval", "1s", "--json")
	// Plant a receipt for today whose cost the ceilings can read, so the day
	// is genuinely spent rather than merely unpriced.
	plantSpentDay(t, p, 1.00)

	// A bounded loop rather than --once: parking has to stop the loop itself,
	// and --until keeps a regression from turning that into a hang.
	run := execJSON[conductorRunDoc](t, p,
		append([]string{"conductor", "run", "--until", "10s", "--json"}, workerArgs...)...)
	if run.Parked == "" {
		t.Fatalf("a spent day did not park the loop: %+v", run)
	}
	if !strings.Contains(run.Parked, "1.00") {
		t.Errorf("park reason = %q, which does not name the ceiling", run.Parked)
	}
	if len(run.Cycles) != 1 || run.Cycles[0].Outcome != "parked" {
		t.Fatalf("parked run reported cycles %+v", run.Cycles)
	}
	if run.Cycles[0].ReceiptID != "" {
		t.Error("a parked cycle produced a receipt")
	}

	status := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	if status.State != "parked" {
		t.Errorf("status after a park = %q", status.State)
	}
	if status.Spend == nil || status.Spend.Runs != 1 || status.Spend.Spent != 1.00 {
		t.Errorf("spend after a park = %+v", status.Spend)
	}
}

// A standing duty, driven end to end through the shipped executable: the
// operator authorizes a dimension, the loop draws the duty it authorizes, and
// the durable receipt records {policy, duty:<name>} as the reason it happened.
//
// This is the scenario issues #88 and #94 are answered by, and what it defends
// is the authorization rather than the analysis. A duty cycle that ran without
// the toggle, ran under an authority nobody could read, or ran the wrong recipe
// would each be autonomy the operator did not grant — and the receipt is where
// that claim survives the process that made it.
func TestConductorDrawsAnAuthorizedDuty(t *testing.T) {
	p := newPhaseB(t)
	worker := fakeWorker(t)
	payload := writeCandidatePayload(t, p)
	workerArgs := []string{
		"--worker", worker,
		"--worker-arg", "-result-payload-selector", "--worker-arg", explore.ParamStage,
		"--worker-arg", "-result-payload", "--worker-arg", string(explore.StageExplore) + "=" + payload,
	}
	plantProfile(t, p, "synthetic-profile", 1)

	// Ceilings alone: the duties exist, and none of them is authorized. A wide
	// serendipity floor keeps the chaotic share out of a two-cycle scenario
	// without disabling it — the floor is a protected fraction, not a switch.
	configured := execJSON[conductorConfigDoc](t, p, "conductor", "configure",
		"--per-cycle", "0.50", "--per-day", "5.00", "--interval", "0s", "--floor", "50", "--json")
	if configured.BabelImprovesBabel || configured.BabelTunesItself {
		t.Fatalf("configuring ceilings authorized a duty: %+v", configured)
	}

	// Unauthorized: the loop has nothing on rung two, and the status view says
	// so per duty rather than by omission.
	cold := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	assertLadderShape(t, cold.Rungs)
	if depth := rungDepth(t, cold.Rungs, "policy"); depth != 0 {
		t.Fatalf("policy rung depth = %d with no duty authorized", depth)
	}
	if len(cold.Duties) != 3 {
		t.Fatalf("status reports %d duties, want the three this build knows: %+v",
			len(cold.Duties), cold.Duties)
	}
	for _, duty := range cold.Duties {
		if duty.Enabled || duty.Due || duty.LastDrawn != "" {
			t.Errorf("duty %+v is live before anyone authorized it", duty)
		}
		if duty.Recipe == "" || duty.Dimension == "" {
			t.Errorf("duty %+v does not say what it would run", duty)
		}
	}

	// The operator authorizes the product dimension. Nothing else changes: same
	// ceilings, same stored profile, no new capability, no new grant.
	authorized := execJSON[conductorConfigDoc](t, p, "conductor", "configure",
		"--babel-improves-babel", "--json")
	if !authorized.BabelImprovesBabel || authorized.BabelTunesItself {
		t.Fatalf("authorization = %+v, want the product dimension only", authorized)
	}
	if authorized.PerCycle != 0.50 || authorized.PerDay != 5.00 {
		t.Fatalf("authorizing a duty changed the ceilings: %+v", authorized)
	}

	queued := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	if depth := rungDepth(t, queued.Rungs, "policy"); depth != 2 {
		t.Fatalf("policy rung depth = %d, want the two duties of the authorized dimension", depth)
	}
	if duty := dutyState(t, queued.Duties, "babel-tunes-itself"); duty.Enabled {
		t.Errorf("authorizing one dimension authorized the other: %+v", duty)
	}

	// Cycle one: the duty, run as an ordinary run over this host's corpus.
	first := execJSON[conductorRunDoc](t, p,
		append([]string{"conductor", "run", "--once", "--json"}, workerArgs...)...)
	if len(first.Cycles) != 1 {
		t.Fatalf("--once ran %d cycles: %+v", len(first.Cycles), first.Cycles)
	}
	cycle := first.Cycles[0]
	if cycle.Outcome != "ran" || cycle.Rung != "policy" {
		t.Fatalf("duty cycle = %+v, want a completed policy cycle", cycle)
	}
	if cycle.AuthorityKind != "policy" || cycle.AuthorityRef != "duty:babel-improves-babel" {
		t.Fatalf("duty authority = %s/%s, want policy/duty:babel-improves-babel",
			cycle.AuthorityKind, cycle.AuthorityRef)
	}
	if len(cycle.Recipes) != 1 || cycle.Recipes[0] != "babel-improves-babel" {
		t.Errorf("duty cycle ran %v, want the duty's own recipe", cycle.Recipes)
	}
	if cycle.Invitation != "" {
		t.Errorf("a duty cycle consumed invitation %q", cycle.Invitation)
	}
	if cycle.ReceiptID == "" || cycle.PreparationID == "" {
		t.Fatalf("a duty cycle produced no ordinary run: %+v", cycle)
	}
	// A duty names no corpus slice, which is how "every session this host can
	// see" is expressed, so the cycle row's session count is zero and the note
	// is what states the scope. A number that said 0 with no explanation would
	// read as an analysis of nothing.
	if cycle.Sessions != 0 {
		t.Errorf("duty cycle named %d sessions; a duty draws no slice", cycle.Sessions)
	}
	if !strings.Contains(cycle.Note, "whole corpus") {
		t.Errorf("duty cycle note = %q, want it to state the scope", cycle.Note)
	}
	// The durable half of the claim: months from now the receipt is what says
	// which standing duty spent this budget.
	assertReceiptAuthority(t, p, cycle.ReceiptID, "policy", "duty:babel-improves-babel")

	// Cycle two is the other duty of the same dimension, not the same one
	// again: one toggle, two duties, each on its own daily cadence.
	second := execJSON[conductorRunDoc](t, p,
		append([]string{"conductor", "run", "--once", "--json"}, workerArgs...)...)
	if len(second.Cycles) != 1 {
		t.Fatalf("--once ran %d cycles: %+v", len(second.Cycles), second.Cycles)
	}
	audit := second.Cycles[0]
	if audit.AuthorityRef != "duty:mechanization-audit" {
		t.Fatalf("second cycle authority = %s/%s, want the audit duty",
			audit.AuthorityKind, audit.AuthorityRef)
	}
	assertReceiptAuthority(t, p, audit.ReceiptID, "policy", "duty:mechanization-audit")
	if audit.RunID == cycle.RunID {
		t.Error("two duty cycles shared one run identity")
	}

	// Both authorized duties have now run today, so the cadence closes rung two
	// and the loop falls to its floor rather than repeating a duty.
	third := execJSON[conductorRunDoc](t, p,
		append([]string{"conductor", "run", "--once", "--json"}, workerArgs...)...)
	if len(third.Cycles) != 1 {
		t.Fatalf("--once ran %d cycles: %+v", len(third.Cycles), third.Cycles)
	}
	if rung := third.Cycles[0].Rung; rung == "policy" {
		t.Fatalf("a duty was drawn twice within its cadence: %+v", third.Cycles[0])
	}

	// And the status view agrees with the journal: the duties that ran say when,
	// the one nobody authorized still says how to authorize it.
	final := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json", "--cycles", "5")
	improves := dutyState(t, final.Duties, "babel-improves-babel")
	if !improves.Enabled || improves.Due || improves.LastDrawn == "" {
		t.Errorf("improves-babel duty after its draw = %+v", improves)
	}
	if !strings.Contains(improves.Note, "next draw after") {
		t.Errorf("duty note %q does not say when it comes back", improves.Note)
	}
	tunes := dutyState(t, final.Duties, "babel-tunes-itself")
	if tunes.Enabled || tunes.Due || tunes.LastDrawn != "" {
		t.Errorf("unauthorized duty = %+v", tunes)
	}
	if !strings.Contains(tunes.Note, "--babel-tunes-itself") {
		t.Errorf("an off duty's note %q does not name the flag that authorizes it", tunes.Note)
	}
	if depth := rungDepth(t, final.Rungs, "policy"); depth != 0 {
		t.Errorf("policy rung depth = %d after both duties ran today", depth)
	}

	// Withdrawing the dimension takes both duties off the ladder again, which is
	// the property that makes the toggle a real authorization rather than a
	// one-way door.
	withdrawn := execJSON[conductorConfigDoc](t, p, "conductor", "configure",
		"--no-babel-improves-babel", "--json")
	if withdrawn.BabelImprovesBabel {
		t.Fatalf("withdrawal left the dimension authorized: %+v", withdrawn)
	}
	after := execJSON[conductorStatusDoc](t, p, "conductor", "status", "--json")
	for _, duty := range after.Duties {
		if duty.Enabled {
			t.Errorf("duty %+v survived the withdrawal", duty)
		}
	}

	// The terminal rendering names the duties too: legibility is not a
	// --json-only feature.
	stdout, _ := p.okExec(t, "conductor", "status")
	for _, want := range []string{"duties", "babel-improves-babel", "mechanization-audit",
		"babel-tunes-itself", "policy"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("conductor status does not mention %q:\n%s", want, stdout)
		}
	}
}

// writeCandidatePayload builds the structured result the synthetic worker emits
// for a conductor cycle: one speculative candidate and nothing else.
//
// It deliberately carries no evidence. §4.2 lets a candidate be speculative, so
// this is a valid result, and it keeps the scenario independent of which
// sessions a serendipity draw happened to pick — a citation into a session the
// drawn slice did not include would be refused for the right reason and make
// the test flaky for the wrong one.
func writeCandidatePayload(t *testing.T, p *phaseB) string {
	t.Helper()
	result := explore.Result{
		Candidates: []explore.Candidate{{
			Ref: "c-1",
			Hypothesis: frontier.HypothesisPayload{
				Statement: "the loop found a shape worth a second look",
				Novelty:   0.6,
				Priority:  0.4,
			},
		}},
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("encode the result payload: %v", err)
	}
	path := filepath.Join(p.root, "candidate.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// plantProfile writes the analysis settings document the profile ceremony
// leaves behind (#86). The ceremony itself hands Code the operator's terminal,
// which no test has; what the conductor needs is the stored reference, and
// planting it is what lets this scenario check that a cycle inherits it.
func plantProfile(t *testing.T, p *phaseB, id string, revision int) {
	t.Helper()
	dir := filepath.Join(p.configHome, "babel")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"schema": 1,
		"profile": map[string]any{
			"id":            id,
			"revision":      revision,
			"configured_at": "2026-08-31T09:00:00.000000000Z",
		},
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// plantSpentDay records a receipt whose profile priced its run in the currency
// the ceilings are in, so the day's budget is genuinely spent.
func plantSpentDay(t *testing.T, p *phaseB, cost float64) {
	t.Helper()
	// A real preparation over a real local session, so the receipt is the
	// record it claims to be rather than a shape pushed into the table.
	prepared := execJSON[prepareDoc](t, p, "prepare", "--harness", "omp", "--json")
	runs, err := runstore.Open(p.durableDir())
	if err != nil {
		t.Fatalf("open the run store: %v", err)
	}
	defer runs.Close()
	ctx := context.Background()
	prep, err := runs.Preparation(ctx, runstore.PreparationID(prepared.PreparationID))
	if err != nil {
		t.Fatalf("read the planted preparation: %v", err)
	}
	body := runstore.Body{
		Cookbook: []runstore.CookbookAsset{{
			Kind: runstore.AssetLens,
			Ref:  worker.RecipeRef{ID: "outcome-integrity", Version: 1},
		}},
		Job:    runstore.JobVersions{Job: explore.JobVersion, Prompt: explore.PromptVersion, Schema: worker.ResultSchema},
		Policy: runstore.PolicyVersions{Redaction: explore.RedactionPolicyVersion, Disclosure: explore.DisclosurePolicyVersion},
		// The cost is the profile's own estimate, which is what the ceilings
		// are enforced against: Babel never sees an invoice.
		Worker: &worker.Receipt{
			Profile: worker.ProfileRef{ID: "synthetic-profile", Revision: 1},
			Cost:    worker.Cost{Currency: "USD", EstimatedRun: cost},
		},
		Timing: runstore.Timing{
			StartedAt:  prep.PreparedAt,
			FinishedAt: prep.PreparedAt.Add(1),
		},
	}
	receipt, err := runstore.NewReceipt(runstore.NewReceiptID(), "run-planted-spend", prep,
		runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "command:explore"},
		body, prep.PreparedAt.Add(1))
	if err != nil {
		t.Fatalf("build the planted receipt: %v", err)
	}
	if err := runs.PutReceipt(ctx, receipt); err != nil {
		t.Fatalf("store the planted receipt: %v", err)
	}
}

// assertLadderShape checks that the ladder a status view reports is the ladder
// #96 describes: the operator's invitations, then the policy rung holding #88's
// and #94's standing duties, then the serendipity floor.
func assertLadderShape(t *testing.T, rungs []conductorRungDoc) {
	t.Helper()
	want := []string{"invitation", "policy", "serendipity"}
	if len(rungs) != len(want) {
		t.Fatalf("ladder = %+v, want %v", rungs, want)
	}
	for i, name := range want {
		if rungs[i].Name != name {
			t.Errorf("rung %d = %q, want %q", i, rungs[i].Name, name)
		}
	}
	for _, rung := range rungs {
		if !rung.Implemented {
			t.Errorf("rung %+v reported itself absent; every rung of this ladder is built", rung)
		}
	}
	// Rung two is implemented and still incomplete: the standing duties are
	// here, the spec's attention policy is not, and the note is where a person
	// reads the difference.
	if note := rungs[1].Note; !strings.Contains(note, "duties") ||
		!strings.Contains(note, "attention policy") {
		t.Errorf("the policy rung's note %q does not say what it holds and what it lacks", note)
	}
}

// dutyState finds one duty in a status view.
func dutyState(t *testing.T, duties []conductorDutyDoc, name string) conductorDutyDoc {
	t.Helper()
	for _, duty := range duties {
		if duty.Name == name {
			return duty
		}
	}
	t.Fatalf("no %q duty in %+v", name, duties)
	return conductorDutyDoc{}
}

func rungDepth(t *testing.T, rungs []conductorRungDoc, name string) int {
	t.Helper()
	for _, rung := range rungs {
		if rung.Name == name {
			return rung.Waiting
		}
	}
	t.Fatalf("no %q rung in %+v", name, rungs)
	return 0
}

// assertReceiptAuthority reads a stored receipt and checks the why it carries.
// The document a command printed is not the record; the record is what a
// reviewer will read months later, so the assertion is made against the store.
func assertReceiptAuthority(t *testing.T, p *phaseB, receiptID, kind, ref string) {
	t.Helper()
	if receiptID == "" {
		t.Fatal("no receipt to check the authority of")
	}
	runs, err := runstore.Open(p.durableDir())
	if err != nil {
		t.Fatalf("open the run store: %v", err)
	}
	defer runs.Close()
	receipt, err := runs.Receipt(context.Background(), runstore.ReceiptID(receiptID))
	if err != nil {
		t.Fatalf("read receipt %s: %v", receiptID, err)
	}
	if !receipt.Header.Authority.Recorded() {
		t.Fatalf("receipt %s records no authority", receiptID)
	}
	if string(receipt.Header.Authority.Kind) != kind || receipt.Header.Authority.Ref != ref {
		t.Errorf("receipt %s authority = %s/%s, want %s/%s", receiptID,
			receipt.Header.Authority.Kind, receipt.Header.Authority.Ref, kind, ref)
	}
}
