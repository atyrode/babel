package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/title"
	"github.com/atyrode/babel/internal/transcript"
)

// The inferred-title path, and why it looks nothing like an analysis run.
//
// A better Codex title is a summary, and a summary needs a model. The obvious
// two implementations were both wrong, for different reasons.
//
// It is not an analysis run. That path exists to let a worker reason over
// evidence: it grants capabilities, brokers tool requests, demands a sandbox
// (worker.SandboxedRun), and commits an immutable receipt whose result schema
// is `babel.analysis-result/1` - findings, hypotheses, proposals. A title is
// none of those. Riding that path would mean either recording titles as
// hypotheses, which pollutes the frontier and the review queue with records
// nobody wrote, or changing the worker protocol to carry a second result
// schema. The sandbox in particular protects against a worker that reads and
// executes; a titler reads nothing - Babel hands it a string.
//
// It is also not a direct provider call from inside Babel. Babel has no
// provider client anywhere, and that is a decision rather than a gap: SPEC.md
// §2.6 and decision 18 put the provider, the model, the credential and the
// sandbox inside Code, `analysisSettings` stores "a location, not a
// configuration", and worker.Configure actively refuses credential-shaped
// metadata. Giving Babel an API key and an endpoint would make it hold its
// first credential and open a second egress path with no disclosure surface.
//
// What is left is the smallest thing that keeps every one of those
// disciplines: the operator configures one profile in Code's own interface
// (titlescmd.go), Babel shows him exactly what would be sent, and only on
// --confirm does it hand that material to the launch he confirmed.

const sessionsTitleUsage = `Usage: babel sessions title <command> [flags]

Commands:
  infer     have an external model write titles for sessions that lack a good one
  clear     withdraw previously inferred titles

A title's provenance is always reported alongside it:

  recorded  the harness wrote this title in the session's own files
  derived   babel computed it offline from the session's records, for free
  inferred  a model wrote it, and session material left this machine

Run "babel sessions title <command> -h" for a command's flags.
`

const sessionsTitleInferUsage = `Usage: babel sessions title infer [flags] [SELECTOR...]

Sends a bounded excerpt of selected sessions to the model an operator
configured for titles, and records the results as "inferred".

This is the only path in babel that pays a provider for session metadata, and
nothing triggers it but this command: it is never part of a scan, a describe,
"archive push", or the hourly timer. Two gates stand in front of it, and they
ask different questions.

First, configuration. The profile that writes titles is chosen once, in Code's
own interface, by an operator holding the terminal:

  babel titles configure

Until that has happened --confirm refuses. Afterwards inference uses exactly
the stored reference - no flag here names a model, a provider or a command -
and changing it means running that ceremony again (issue #86).

Second, disclosure. Without --confirm nothing is launched and nothing leaves
the machine: the command prints the launch it would run, the profile it would
run under, how many sessions it would send, how many bytes of session text
that is, and the excerpt of each one, then exits. Read it, then decide.

The titler protocol is one JSON object per line in each direction. Babel writes
{"selector","harness","workspace","excerpt"} on stdin and reads
{"selector","title"} - or {"selector","error"} - on stdout, in any order. A
"model" field on a response is recorded as the identity that produced the
title. A response for a selector babel did not send is refused.

By default only sessions whose title is derived or absent are offered; a title
the harness itself recorded is never replaced, because babel's guess does not
outrank the session's own record.

Flags:
  --harness NAME       restrict to one harness: omp, codex, or claude
  --roots DIR[,DIR]    scan these roots instead of the adapters' defaults
  --limit N            send at most N sessions (default 20, 0 for no bound)
  --untitled-only      offer only sessions that have no title at all
  --excerpt-runes N    bound each session's excerpt (default 1200)
  --timeout DURATION   give up on the titler after this long (default 10m)
  --confirm            actually run the titler and record what it returns
  --json               emit the plan or the outcome as JSON on stdout

Inferred titles are durable: they live in the durable database beside the
analysis records, not in the rebuildable session catalog, because a title a
model wrote is the one value here that a rescan cannot recover. They survive
every reconfiguration of the profile that wrote them.
`

const sessionsTitleClearUsage = `Usage: babel sessions title clear (--all | SELECTOR...) --yes

Removes inferred titles. The session's derived or recorded title returns,
because inference only ever overlaid it.

Flags:
  --all                remove every inferred title
  --yes                required: confirm the removal
  --json               emit the outcome as JSON on stdout
`

// Bounds on the material one titler invocation may send and receive. They are
// small because everything about this path is meant to be readable before it
// runs: a plan an operator cannot finish reading is not a disclosure.
const (
	defaultInferLimit        = 20
	defaultExcerptRunes      = 1200
	defaultTitlerTimeout     = 10 * time.Minute
	maxExcerptEvents         = 6
	maxExcerptEventRunes     = 600
	transcriptExcerptRecords = 64
	maxTitlerResponseLine    = 64 << 10
)

// sessionsTitle routes `babel sessions title <verb>`.
func (a *app) sessionsTitle(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "sessions title requires a subcommand", usage: sessionsTitleUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, sessionsTitleUsage)
		return nil
	case "infer":
		return a.sessionsTitleInfer(ctx, args[1:])
	case "clear":
		return a.sessionsTitleClear(ctx, args[1:])
	default:
		return &usageError{
			msg:   fmt.Sprintf("unknown sessions title subcommand %q", args[0]),
			usage: sessionsTitleUsage,
		}
	}
}

// titlerRequest is one line babel writes to a titler's stdin. Every field is
// something the operator has already been shown.
type titlerRequest struct {
	Selector  string `json:"selector"`
	Harness   string `json:"harness"`
	Workspace string `json:"workspace,omitempty"`
	Excerpt   string `json:"excerpt"`
}

// titlerResponse is one line babel reads from a titler's stdout. Title and
// Error are both optional so a titler can decline one session without failing
// the batch, which matters when a single unreadable session would otherwise
// waste every other session's tokens.
type titlerResponse struct {
	Selector string `json:"selector"`
	Title    string `json:"title,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

// inferCandidate is one session the command may offer to a titler.
type inferCandidate struct {
	Selector   string  `json:"selector"`
	Harness    string  `json:"harness"`
	Workspace  *string `json:"workspace"`
	Title      *string `json:"title"`
	Provenance *string `json:"title_provenance"`
	Excerpt    string  `json:"excerpt"`
	// ExcerptBytes is what this session contributes to the disclosure, in the
	// unit that actually leaves the machine.
	ExcerptBytes int `json:"excerpt_bytes"`
}

// inferPlan is what a --json invocation without --confirm returns: exactly the
// material that would be sent, and nothing has been sent.
type inferPlan struct {
	// Titler is the argv that would run, and Profile the reference it would
	// run under. Both are empty exactly when no operator has configured
	// title inference on this machine, which is the state in which --confirm
	// refuses.
	Titler       []string         `json:"titler,omitempty"`
	Profile      string           `json:"profile,omitempty"`
	Confirmed    bool             `json:"confirmed"`
	Sessions     []inferCandidate `json:"sessions"`
	TotalBytes   int              `json:"total_bytes"`
	Skipped      int              `json:"skipped_recorded"`
	NoExcerpt    int              `json:"skipped_no_excerpt"`
	Bounded      bool             `json:"bounded_by_limit"`
	Results      []inferResult    `json:"results,omitempty"`
	RecordedRows int              `json:"recorded,omitempty"`
}

// inferResult is one titler outcome.
type inferResult struct {
	Selector string `json:"selector"`
	Title    string `json:"title,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (a *app) sessionsTitleInfer(ctx context.Context, args []string) error {
	c := newCmd("sessions title infer", sessionsTitleInferUsage)
	var sf scanFlags
	sf.bindHarness(c)
	sf.bindRoots(c)
	limit := c.fs.Int("limit", defaultInferLimit, "send at most this many sessions")
	untitledOnly := c.fs.Bool("untitled-only", false, "offer only sessions with no title")
	excerptRunes := c.fs.Int("excerpt-runes", defaultExcerptRunes, "bound each excerpt")
	timeout := c.fs.Duration("timeout", defaultTitlerTimeout, "give up on the titler after this long")
	confirm := c.fs.Bool("confirm", false, "actually run the titler")
	asJSON := c.fs.Bool("json", false, "emit the plan or outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if *limit < 0 {
		return c.usagef("--limit must not be negative")
	}
	if *excerptRunes <= 0 {
		return c.usagef("--excerpt-runes must be positive")
	}

	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	titler := settings.Titles
	// An unconfigured machine is refused before a single session is read.
	// Scanning the corpus first would spend the operator's time to reach a
	// conclusion the settings document already had, and the refusal is the
	// same either way: no model has been chosen for this.
	//
	// The refusal is on --confirm alone. A plan is a local reading of local
	// files that sends nothing, and an operator deciding whether this feature
	// is worth configuring is exactly the person who should be able to see
	// what it would send.
	if titler == nil && *confirm {
		return a.reportTitlesUnconfigured()
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	ads, err := sf.selected(c)
	if err != nil {
		return err
	}
	sessions, _ := a.scan(ctx, ads, sf.rootList())
	wanted := map[string]struct{}{}
	for _, arg := range c.fs.Args() {
		target, err := resolveSelector(c, sessions, arg)
		if err != nil {
			return err
		}
		wanted[target.key()] = struct{}{}
	}

	plan, err := a.buildInferPlan(ctx, sessions, wanted, *untitledOnly, *limit, *excerptRunes)
	if err != nil {
		return err
	}
	if titler != nil {
		plan.Titler = sanitizeAll(titler.command())
		plan.Profile = Sanitize(titler.ref().String())
	}
	plan.Confirmed = *confirm

	if !*confirm {
		if *asJSON {
			return a.emitJSON(plan)
		}
		return a.printInferPlan(plan)
	}
	if len(plan.Sessions) == 0 {
		if *asJSON {
			return a.emitJSON(plan)
		}
		fmt.Fprint(a.stdout, "no session needs an inferred title\n")
		return nil
	}

	results, err := a.runTitler(ctx, plan, titler, *timeout, d.durableDir())
	plan.Results = results
	for _, r := range results {
		if r.Error == "" {
			plan.RecordedRows++
		}
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return a.emitJSON(plan)
	}
	return a.printInferResults(plan)
}

// buildInferPlan selects the sessions worth titling and builds each one's
// excerpt. A session whose title the harness recorded is counted and skipped:
// babel's guess does not outrank the session's own record, and offering it
// would spend the operator's money to overwrite a fact.
func (a *app) buildInferPlan(
	ctx context.Context,
	sessions []localSession,
	wanted map[string]struct{},
	untitledOnly bool,
	limit int,
	excerptRunes int,
) (inferPlan, error) {
	var plan inferPlan
	for _, session := range sessions {
		if len(wanted) > 0 {
			if _, ok := wanted[session.key()]; !ok {
				continue
			}
		}
		desc, err := describe(ctx, session)
		if err != nil {
			a.diagf("warning: %s\n", Sanitize(err.Error()))
			continue
		}
		provenance := provenancePtr(desc.Meta)
		if provenance != nil && *provenance == string(adapterTitleRecorded) {
			plan.Skipped++
			continue
		}
		if untitledOnly && desc.Meta.Title != nil {
			continue
		}
		if limit > 0 && len(plan.Sessions) >= limit {
			plan.Bounded = true
			break
		}
		excerpt := sessionExcerpt(session, excerptRunes)
		if excerpt == "" {
			// Nothing to summarize means nothing to send. Paying a provider to
			// read an empty string would produce a title with no basis, which
			// is worse than the honest absence the session already reports.
			plan.NoExcerpt++
			continue
		}
		plan.Sessions = append(plan.Sessions, inferCandidate{
			Selector:     Sanitize(session.key()),
			Harness:      Sanitize(session.src.Harness),
			Workspace:    sanitizePtr(desc.Meta.Workspace),
			Title:        sanitizePtr(desc.Meta.Title),
			Provenance:   provenance,
			Excerpt:      excerpt,
			ExcerptBytes: len(excerpt),
		})
		plan.TotalBytes += len(excerpt)
	}
	sort.Slice(plan.Sessions, func(i, j int) bool {
		return plan.Sessions[i].Selector < plan.Sessions[j].Selector
	})
	return plan, nil
}

// adapterTitleRecorded is the provenance value that makes a session ineligible.
// It is spelled out here rather than imported as a constant so this file does
// not depend on the adapter package for one string; the vocabulary is fixed by
// migrations/0005 and asserted by test.
const adapterTitleRecorded = "recorded"

// sessionExcerpt builds the bounded material one session contributes.
//
// It is assembled from the leading message events of the session's own log,
// through the same reader the web transcript view uses, so what a titler sees
// is what the operator can read for himself. Two filters apply: only user and
// assistant messages, because tool output and reasoning are the bulk of a log
// and the worst value per token; and, for Codex, the harness's injected context
// blocks are dropped, because paying a provider to summarize
// `<permissions instructions>` would disclose nothing and cost the same.
func sessionExcerpt(session localSession, maxRunes int) string {
	_, events, err := transcript.Events(session.src.PrimaryPath, session.src.Harness,
		0, transcriptExcerptRecords)
	if err != nil {
		return ""
	}
	var b strings.Builder
	used := 0
	kept := 0
	for _, e := range events {
		if kept >= maxExcerptEvents || used >= maxRunes {
			break
		}
		if e.Kind != "message" || (e.Role != "user" && e.Role != "assistant") {
			continue
		}
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		if session.src.Harness == codex.HarnessName && codex.InjectedContext(text) {
			continue
		}
		budget := min(maxExcerptEventRunes, maxRunes-used)
		text = truncateRunes(text, budget)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Role)
		b.WriteString(": ")
		b.WriteString(text)
		used += utf8.RuneCountInString(text)
		kept++
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// runTitler launches the configured titler, streams the plan to it, and
// records the titles it returns.
//
// The launch is the stored one, whole: the executable an operator confirmed a
// profile in, that profile's reference, and nothing this invocation chose. The
// one variable that could override the reference is dropped from the child's
// environment for the same reason the ceremony drops it (modelEnv).
//
// Every response is checked against what was actually sent. A titler is an
// external command whose output is untrusted in the same way a session log is,
// and a response naming a selector babel did not offer would let it write a
// title onto an arbitrary session - including one whose title the harness
// recorded, which this command refuses to touch.
func (a *app) runTitler(
	ctx context.Context,
	plan inferPlan,
	titler *titlesRecord,
	timeout time.Duration,
	durableDir string,
) ([]inferResult, error) {
	store, err := title.Open(durableDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, titler.Worker, titler.launch()...)
	env, dropped := modelEnv()
	cmd.Env = env
	if dropped {
		a.diagf("ignoring $%s: titles run under the profile %s, which the operator configured\n",
			selectionStateEnv, Sanitize(titler.ref().String()))
	}
	cmd.Stderr = a.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("titler stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("titler stdout: %w", err)
	}
	// The attribution stored with each title is the launch that produced it,
	// executable and reference together. The settings document is
	// reconfigurable and the row is not: a title has to keep saying what wrote
	// it after the operator has chosen something else.
	attribution := titler.Worker + " " + titler.ref().String()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the configured titler %s: %w (\"babel titles show\" prints the stored launch, \"babel titles configure\" replaces it)",
			Sanitize(titler.Worker), err)
	}

	offered := make(map[string]struct{}, len(plan.Sessions))
	writeErr := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(stdin)
		for _, s := range plan.Sessions {
			if err := enc.Encode(titlerRequest{
				Selector:  s.Selector,
				Harness:   s.Harness,
				Workspace: derefOr(s.Workspace, ""),
				Excerpt:   s.Excerpt,
			}); err != nil {
				writeErr <- err
				stdin.Close()
				return
			}
		}
		writeErr <- stdin.Close()
	}()
	for _, s := range plan.Sessions {
		offered[s.Selector] = struct{}{}
	}

	var results []inferResult
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxTitlerResponseLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp titlerResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			results = append(results, inferResult{Error: "titler wrote a line that is not a response object"})
			continue
		}
		results = append(results, a.recordInferred(ctx, store, offered, resp, attribution))
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Selector < results[j].Selector })

	if err := <-writeErr; err != nil && !errors.Is(err, os.ErrClosed) {
		a.diagf("warning: titler stopped reading: %s\n", Sanitize(err.Error()))
	}
	if scanErr != nil {
		return results, fmt.Errorf("read titler output: %w", scanErr)
	}
	if waitErr != nil {
		return results, fmt.Errorf("titler %s failed: %w", Sanitize(attribution), waitErr)
	}
	return results, nil
}

// recordInferred validates and stores one titler response.
func (a *app) recordInferred(
	ctx context.Context,
	store *title.Store,
	offered map[string]struct{},
	resp titlerResponse,
	titler string,
) inferResult {
	selector := Sanitize(resp.Selector)
	if _, ok := offered[resp.Selector]; !ok {
		return inferResult{
			Selector: selector,
			Error:    "titler answered for a selector that was not sent",
		}
	}
	if resp.Error != "" {
		return inferResult{Selector: selector, Error: Sanitize(resp.Error)}
	}
	normalized, err := title.Normalize(resp.Title)
	if err != nil {
		return inferResult{Selector: selector, Error: Sanitize(err.Error())}
	}
	if err := store.Put(ctx, title.Inferred{
		Selector:   resp.Selector,
		Title:      normalized,
		Titler:     titler,
		Model:      resp.Model,
		InferredAt: time.Now().UTC(),
	}); err != nil {
		return inferResult{Selector: selector, Error: Sanitize(err.Error())}
	}
	return inferResult{
		Selector: selector,
		Title:    Sanitize(normalized),
		Model:    Sanitize(resp.Model),
	}
}

// printInferPlan writes the disclosure. It is what stands between the operator
// and a bill, so it states the three facts a decision needs - what runs, how
// much leaves, and what exactly leaves - and it prints the excerpts in full
// rather than summarizing them.
func (a *app) printInferPlan(plan inferPlan) error {
	rows := [][2]string{
		{"titler", orMissing(Sanitize(strings.Join(plan.Titler, " ")))},
		{"profile", orMissing(plan.Profile)},
		{"sessions to send", fmt.Sprint(len(plan.Sessions))},
		{"session text to send", fmt.Sprintf("%d bytes", plan.TotalBytes)},
		{"skipped, harness recorded a title", fmt.Sprint(plan.Skipped)},
		{"skipped, nothing to summarize", fmt.Sprint(plan.NoExcerpt)},
		{"bounded by --limit", yesNo(plan.Bounded, "yes", "no")},
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	if len(plan.Sessions) == 0 {
		fmt.Fprint(a.stdout, "\nno session needs an inferred title; nothing would be sent\n")
		return nil
	}
	fmt.Fprint(a.stdout, "\nthis material would leave the machine:\n")
	for _, s := range plan.Sessions {
		fmt.Fprintf(a.stdout, "\n%s  [%s, title now: %s]\n", s.Selector,
			derefOrMissing(s.Provenance), derefOrMissing(s.Title))
		for line := range strings.SplitSeq(s.Excerpt, "\n") {
			fmt.Fprintf(a.stdout, "  | %s\n", Sanitize(line))
		}
	}
	// The closing line is the operator's next move, so it names the gate that
	// is actually in his way: the ceremony when nothing is configured, and
	// --confirm once something is.
	if plan.Profile == "" {
		fmt.Fprint(a.stdout, "\nnothing has been sent, and --confirm would refuse: no title-inference profile is configured.\n")
		fmt.Fprint(a.stdout, "run \"babel titles configure\" to choose one in Code's own interface, then re-run with --confirm.\n")
		return nil
	}
	fmt.Fprint(a.stdout, "\nnothing has been sent. re-run with --confirm to send it.\n")
	return nil
}

func (a *app) printInferResults(plan inferPlan) error {
	tableRows := make([][]string, 0, len(plan.Results))
	for _, r := range plan.Results {
		outcome := r.Title
		if r.Error != "" {
			outcome = "refused: " + r.Error
		}
		tableRows = append(tableRows, []string{r.Selector, r.Model, outcome})
	}
	if err := writeTable(a.stdout, []string{"SELECTOR", "MODEL", "TITLE"}, tableRows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "\nrecorded %d inferred title(s)\n", plan.RecordedRows)
	return nil
}

// clearResult is `babel sessions title clear`.
type clearResult struct {
	Removed  []string `json:"removed"`
	Absent   []string `json:"absent,omitempty"`
	AllCount int      `json:"removed_count"`
}

func (a *app) sessionsTitleClear(ctx context.Context, args []string) error {
	c := newCmd("sessions title clear", sessionsTitleClearUsage)
	all := c.fs.Bool("all", false, "remove every inferred title")
	yes := c.fs.Bool("yes", false, "confirm the removal")
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selectors := c.fs.Args()
	if *all == (len(selectors) > 0) {
		return c.usagef("give either --all or one or more selectors, not both")
	}
	if !*yes {
		return c.usagef("--yes is required to remove inferred titles")
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	store, err := title.Open(d.durableDir())
	if err != nil {
		return err
	}
	defer store.Close()

	if *all {
		existing, err := store.All(ctx)
		if err != nil {
			return err
		}
		selectors = make([]string, 0, len(existing))
		for selector := range existing {
			selectors = append(selectors, selector)
		}
		sort.Strings(selectors)
	}

	var res clearResult
	for _, selector := range selectors {
		removed, err := store.Delete(ctx, selector)
		if err != nil {
			return err
		}
		if removed {
			res.Removed = append(res.Removed, Sanitize(selector))
			continue
		}
		res.Absent = append(res.Absent, Sanitize(selector))
	}
	res.AllCount = len(res.Removed)
	if *asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "removed %d inferred title(s)\n", res.AllCount)
	for _, selector := range res.Absent {
		fmt.Fprintf(a.stdout, "no inferred title for %s\n", selector)
	}
	return nil
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, " ") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// inferredOverlay resolves which title a session displays, and is the single
// authority for that decision.
//
// An inferred title overlays a derived one and never a recorded one: the store
// only ever holds sessions `title infer` offered, and that command refuses to
// offer a session whose harness recorded a title. The check is repeated here
// anyway, because the two guards protect against different mistakes - that one
// stops the spend, this one stops a stale row from misattributing a harness's
// own record - and a rule enforced at one end of a pipeline is a rule that
// stops holding the day someone adds a second writer.
type inferredOverlay map[string]title.Inferred

// loadInferredOverlay reads the durable inferred titles. A missing or
// unreadable store is not an error: the derived titles below it are complete on
// their own, and failing a session listing because an optional overlay could
// not be read would make a paid extra cost the operator the whole command.
func (a *app) loadInferredOverlay(ctx context.Context, durableDir string) inferredOverlay {
	overlay, err := readInferredOverlay(ctx, durableDir)
	if err != nil {
		a.diagf("warning: inferred titles unavailable: %s\n", Sanitize(err.Error()))
	}
	return overlay
}

// readInferredOverlay is the same read without a diagnostic stream, for the
// callers that have no *app — the web scan coordinator among them. Both go
// through it so the web listing and the terminal listing cannot show different
// titles for the same session.
func readInferredOverlay(ctx context.Context, durableDir string) (inferredOverlay, error) {
	store, err := title.Open(durableDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	all, err := store.All(ctx)
	if err != nil {
		return nil, err
	}
	return all, nil
}

// apply returns the title and provenance one session should display.
func (o inferredOverlay) apply(selector string, current, provenance *string) (*string, *string) {
	if len(o) == 0 {
		return current, provenance
	}
	if provenance != nil && *provenance == adapterTitleRecorded {
		return current, provenance
	}
	in, ok := o[selector]
	if !ok || in.Title == "" {
		return current, provenance
	}
	t := Sanitize(in.Title)
	p := "inferred"
	return &t, &p
}

// derefOr returns *p, or fallback when p is nil.
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}
