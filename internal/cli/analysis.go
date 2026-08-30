package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// Phase B state placement (SPEC.md §9). The hypothesis frontier, the run
// receipts, the review log and the Reality Ledger are durable and pending
// remote sync, so they live in the data directory beside the catalog and
// share one `durable.db`. The retrieval index is derived from the corpus and
// is therefore a cache: losing it costs a re-index, losing the durable file
// loses analysis, and putting them in one place would invite treating both
// the same way.
func (d dirs) durableDir() string { return d.data }

func (d dirs) indexDir() string { return d.cache }

// analysisSchema versions the analysis settings document this package owns.
const analysisSchema = 1

// analysisFile is where the Code profile reference is kept. It sits beside
// storage.json rather than in the data directory because it is
// configuration: nothing rebuilds it, and `sessions prune --local` must
// never be able to reach it.
const analysisFile = "analysis.json"

// maxOperatorIDLen bounds an attributed operator identity. The identity is
// written into immutable records and rendered back on every history, so it
// is bounded on the way in rather than truncated on the way out.
const maxOperatorIDLen = 128

// analysisSettings is everything Babel keeps about analysis execution, and
// it is deliberately almost nothing. §2.6 and decision 18 put the provider,
// the model, the credential and the sandbox inside Code; Babel stores the
// profile reference Code returned plus the non-secret metadata it reported
// alongside it, and where the worker executable is.
type analysisSettings struct {
	Schema int `json:"schema"`
	// Worker and WorkerArgs are how this machine launches the Code
	// capability. They are a location, not a configuration: nothing here
	// describes how analysis runs.
	Worker     string   `json:"worker,omitempty"`
	WorkerArgs []string `json:"worker_args,omitempty"`
	// Profile is the reference `analysis profile configure` stored. It is a
	// pointer so "never configured" and "configured with empty values" are
	// different documents.
	Profile *profileRecord `json:"profile,omitempty"`
}

// profileRecord is the Code profile reference plus the metadata §2.6 lets
// Babel keep. The provider configuration behind the reference is Code's and
// never appears here; worker.Configure already refuses credential-shaped
// metadata, so what reaches this file has passed that check.
type profileRecord struct {
	ID                string            `json:"id"`
	Revision          int               `json:"revision"`
	ConfiguredAt      string            `json:"configured_at"`
	WorkerName        string            `json:"worker_name,omitempty"`
	WorkerVersion     string            `json:"worker_version,omitempty"`
	ProtocolVersion   int               `json:"protocol_version,omitempty"`
	Disclosure        string            `json:"disclosure,omitempty"`
	RedactionRequired bool              `json:"redaction_required"`
	Capabilities      []string          `json:"capabilities,omitempty"`
	Currency          string            `json:"cost_currency,omitempty"`
	EstimatedRun      float64           `json:"cost_estimated_run,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// ref returns the profile reference a run is launched with.
func (p *profileRecord) ref() worker.ProfileRef {
	return worker.ProfileRef{ID: p.ID, Revision: p.Revision}
}

// analysisPath resolves the settings document's location.
func analysisPath() (string, error) {
	base := config.Path()
	if base == "" {
		return "", errors.New("cannot resolve the configuration directory for analysis settings")
	}
	return filepath.Join(filepath.Dir(base), analysisFile), nil
}

// loadAnalysisSettings reads the settings document. A missing file is not an
// error: an unconfigured machine is the normal state, and every command that
// needs a profile says so itself rather than failing here.
func loadAnalysisSettings() (analysisSettings, error) {
	path, err := analysisPath()
	if err != nil {
		return analysisSettings{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return analysisSettings{Schema: analysisSchema}, nil
	}
	if err != nil {
		return analysisSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s analysisSettings
	if err := json.Unmarshal(data, &s); err != nil {
		// The path is named and the content is not, matching storage.json's
		// rule: a settings document is not a place to quote values from.
		return analysisSettings{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if s.Schema > analysisSchema {
		return analysisSettings{}, fmt.Errorf("%s: schema %d, this build reads %d", path, s.Schema, analysisSchema)
	}
	return s, nil
}

// saveAnalysisSettings replaces the settings document atomically, so an
// interrupted write leaves the previous reference rather than a truncated
// one.
func saveAnalysisSettings(s analysisSettings) (string, error) {
	path, err := analysisPath()
	if err != nil {
		return "", err
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	s.Schema = analysisSchema
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode analysis settings: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	return path, nil
}

// repeatedFlag collects a flag given more than once, which is how a recipe
// set and a worker argument list are supplied without inventing a separator
// that a value could contain.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, " ") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// operatorFlags carry the attributed operator identity every Phase B
// mutation requires. §4.7 makes a disposition attributed guidance and §4.8
// makes an answer and a plan acceptance attributed acts, so an unattributed
// mutation is refused: defaulting to a placeholder would record that
// something was decided without recording that anyone decided it.
type operatorFlags struct{ operator string }

func (of *operatorFlags) bind(c *cmd) {
	c.fs.StringVar(&of.operator, "operator", "", "operator identity this decision is attributed to")
}

// operatorHint is the one-line remedy attached to every refused mutation.
const operatorHint = `pass --operator ID or set $BABEL_OPERATOR`

// resolve returns the operator identity, refusing the invocation when there
// is none.
func (of *operatorFlags) resolve(c *cmd) (string, error) {
	id := firstNonEmpty(of.operator, os.Getenv("BABEL_OPERATOR"))
	switch {
	case id == "":
		return "", c.usagef("this command records an attributed decision (SPEC.md §4.7) and no operator identity was given; %s", operatorHint)
	case utf8.RuneCountInString(id) > maxOperatorIDLen:
		return "", c.usagef("operator identity is longer than %d characters", maxOperatorIDLen)
	}
	for _, r := range id {
		if unsafeRune(r) {
			return "", c.usagef("operator identity contains a control, bidi, or invisible character")
		}
	}
	return id, nil
}

// authority converts a resolved identity into the type review's decision
// methods accept. The conversion is here rather than at each call site so
// that no command can reach Decide without having gone through resolve.
func authorityFor(id string) (review.Authority, error) {
	by, err := review.NewAuthority(id)
	if err != nil {
		return review.Authority{}, fmt.Errorf("operator identity: %w", err)
	}
	return by, nil
}

// workerFlags select the Code executable that speaks the
// babel.analysis-worker protocol. Babel launches it; it never chooses a
// provider or a model (SPEC.md §2.6).
type workerFlags struct {
	binary string
	args   repeatedFlag
}

func (wf *workerFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&wf.binary, "worker", "", "Code executable speaking the babel.analysis-worker protocol")
	fs.Var(&wf.args, "worker-arg", "extra argument for the worker executable; repeatable")
}

// resolve fixes the worker launch template from flags, the environment, and
// the stored settings, in that order.
//
// Stored arguments travel with the stored binary and nothing else: a
// `--worker` override names a different executable, and handing it another
// executable's arguments would launch it in a mode nobody asked for.
func (wf *workerFlags) resolve(s analysisSettings) (worker.Config, bool) {
	binary := firstNonEmpty(wf.binary, os.Getenv("BABEL_ANALYSIS_WORKER"), s.Worker)
	if binary == "" {
		return worker.Config{}, false
	}
	args := []string(wf.args)
	if len(args) == 0 && binary == s.Worker {
		args = slices.Clone(s.WorkerArgs)
	}
	return worker.Config{Binary: binary, Args: args}, true
}

// reportNoWorker explains an absent Code capability and reports that the
// explanation has been given.
//
// The message is the product here. Code does not implement this protocol
// yet, so an operator who runs `babel explore` today hits this path on a
// correctly installed Babel; it has to read as a stated boundary with a
// remedy rather than as a malfunction, and it has to say what still works.
func (a *app) reportNoWorker() error {
	fmt.Fprint(a.stderr, `babel: no Code analysis worker is available, so this exploration cannot start.

Exploration runs inside Code, which owns the analysis profile, the provider
credential, and the sandbox (SPEC.md §2.6). Babel launches an executable
speaking the babel.analysis-worker protocol and never chooses a model
itself. This machine has none configured.

To name one:
  babel analysis profile configure --worker PATH
  babel explore --worker PATH --preparation ID
  or set $BABEL_ANALYSIS_WORKER

Code does not implement this protocol yet, so this is the expected state
today rather than a fault. Everything that does not need a worker still
works: archive, sessions, prepare, hypotheses, findings, review, export,
reality, and cookbook.
`)
	return errReported
}

// reportWorkerFailure explains a worker that was configured but could not be
// used. It is separate from reportNoWorker because "you have not configured
// one" and "the one you configured did not work" are different problems with
// different remedies, and collapsing them would send an operator to
// reconfigure a worker that is already configured.
//
// It routes SPEC.md §10's platform refusal to its own explanation for the same
// reason. "This platform does not run analysis at all" is not a fault in the
// worker, and printing it under a heading that blames the worker would send an
// operator to debug an executable that is behaving correctly.
func (a *app) reportWorkerFailure(binary string, err error) error {
	var refusal worker.PlatformRefusal
	if errors.As(err, &refusal) {
		return a.reportPlatformRefusal(refusal)
	}
	fmt.Fprintf(a.stderr, `babel: the Code analysis worker could not run this exploration.

  worker: %s
  reason: %s

Babel launches the worker and supervises the protocol; it does not
implement analysis (SPEC.md §2.6). Nothing was published and no source
repository was touched. Durable records the run committed before the
failure are kept, and re-running with the same --run-id resumes rather
than duplicating them.
`, Sanitize(binary), Sanitize(err.Error()))
	return errReported
}

// reportPlatformRefusal explains §10's platform gate and reports that the
// explanation has been given.
//
// The message is the product here, the same way reportNoWorker's is. An
// operator on an unqualified platform has a correctly installed Babel and a
// correctly behaving worker, and the only true account of what happened is that
// exploration is disabled on this platform by design. So it says which platform
// it is, that no backend has passed its escape scenario there, that no worker
// change lifts the limit, and what the machine still does — because refusing
// exploration is not refusing Babel.
func (a *app) reportPlatformRefusal(refusal worker.PlatformRefusal) error {
	fmt.Fprintf(a.stderr, `babel: exploration is refused on %s: this platform has no qualified
sandbox backend.

  reason: %s

Exploration runs provider inference over archived material inside a
sandbox Code owns, and SPEC.md §10 disables analysis on any platform whose
backend has not been driven through its escape scenario. None has passed on
%s, so Babel refuses the run instead of executing behind a boundary nobody
has tested. This is a stated limit rather than a fault: your installation
is fine, the worker is fine, and no worker or configuration change lifts it
— a platform becomes eligible by passing the scenario.

Nothing was published, no source repository was touched, and no session
material was sent anywhere.

Everything that does not explore still works on this platform: web,
archive init/push/status/verify, sessions list/inspect/fetch, prepare,
hypotheses, findings, review queue/decide/history, export, reality,
and cookbook. The archive is portable, so the same preparation explores on
a platform that does have a qualified backend.
`, Sanitize(refusal.UnqualifiedPlatform()), Sanitize(refusal.Error()),
		Sanitize(refusal.UnqualifiedPlatform()))
	return errReported
}

// analysisUsage is the profile command group.
const analysisUsage = `Usage: babel analysis profile <command> [flags]

Commands:
  configure    launch Code's configuration-only capability and store the
               reference it returns
  show         show the stored profile reference and its metadata

Babel does not own an analysis profile (SPEC.md §2.6, decision 18). The
provider, the model, the credential, the prompt budget, and the sandbox all
belong to Code. These commands launch Code's own configuration capability
and store only the profile ID and revision it returns, plus the non-secret
privacy, cost, and capability metadata it reports. Nothing here edits a
profile, and no Babel command can read one.
`

const analysisConfigureUsage = `Usage: babel analysis profile configure [flags]

Launches the Code worker in configuration-only mode. Code opens its own
dials, saves the profile under its own ownership, and returns a reference.
Babel writes that reference plus the non-secret metadata to analysis.json
and nothing else: it never sees or stores the provider configuration behind
it (SPEC.md §2.6, decision 18).

Nothing is analysed and no session is read by this command.

Flags:
  --worker PATH        Code executable speaking babel.analysis-worker
                       (default $BABEL_ANALYSIS_WORKER, else the stored one)
  --worker-arg ARG     extra argument for the worker; repeatable
  --timeout D          bound the configuration exchange (default 2m)
  --json               emit the stored reference as JSON on stdout
`

const analysisShowUsage = `Usage: babel analysis profile show [--json]

Shows the profile reference Babel stored, the worker executable it was
obtained from, and the non-secret metadata Code reported. This is the whole
of what Babel knows about analysis execution; the profile itself lives in
Code (SPEC.md §2.6).

Flags:
  --json    emit the stored reference as JSON on stdout
`

// analysis routes `babel analysis <noun> <verb>`.
func (a *app) analysis(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "analysis requires a subcommand", usage: analysisUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, analysisUsage)
		return nil
	case "profile":
		return a.analysisProfile(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown analysis subcommand %q", args[0]), usage: analysisUsage}
	}
}

func (a *app) analysisProfile(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "analysis profile requires a subcommand", usage: analysisUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, analysisUsage)
		return nil
	case "configure":
		return a.analysisProfileConfigure(ctx, args[1:])
	case "show":
		return a.analysisProfileShow(args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown analysis profile subcommand %q", args[0]), usage: analysisUsage}
	}
}

// profileResult is the machine-readable profile document, shared by
// configure and show so a script sees one shape whichever produced it.
type profileResult struct {
	Configured bool           `json:"configured"`
	Worker     string         `json:"worker,omitempty"`
	WorkerArgs []string       `json:"worker_args,omitempty"`
	Profile    *profileRecord `json:"profile,omitempty"`
	// Owner states the ownership boundary in the machine-readable document
	// too, because a caller that only ever reads JSON would otherwise see a
	// "profile" object and reasonably conclude Babel owns one.
	Owner string `json:"profile_owner"`
	Path  string `json:"settings_path"`
}

// profileOwner is the one-line statement of decision 18.
const profileOwner = "code"

func (a *app) analysisProfileConfigure(ctx context.Context, args []string) error {
	c := newCmd("analysis profile configure", analysisConfigureUsage)
	var wf workerFlags
	wf.bind(c.fs)
	timeout := c.fs.Duration("timeout", 2*time.Minute, "bound the configuration exchange")
	asJSON := c.fs.Bool("json", false, "emit the stored reference as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	wcfg, ok := wf.resolve(settings)
	if !ok {
		return a.reportNoWorker()
	}
	// Configuration launches nothing and reads nothing, so the sandbox
	// requirement that guards a run does not apply: a configuration-only
	// probe declares no containment because there is nothing to contain.
	unsandboxed := worker.Unsandboxed()
	wcfg.Requirement = &unsandboxed
	wcfg.Diagnostics = &sanitizingWriter{w: a.stderr, prefix: "worker: "}
	client, err := worker.New(wcfg)
	if err != nil {
		return a.reportWorkerFailure(wcfg.Binary, err)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	a.diagf("launching %s in configuration-only mode...\n", Sanitize(wcfg.Binary))
	resolved, err := client.Configure(ctx)
	if err != nil {
		return a.reportWorkerFailure(wcfg.Binary, err)
	}

	record := &profileRecord{
		ID:                resolved.Profile.ID,
		Revision:          resolved.Profile.Revision,
		ConfiguredAt:      formatTime(time.Now().UTC()),
		WorkerName:        resolved.Worker.Name,
		WorkerVersion:     resolved.Worker.Version,
		ProtocolVersion:   resolved.ProtocolVersion,
		Disclosure:        resolved.Privacy.Disclosure,
		RedactionRequired: resolved.Privacy.RedactionRequired,
		Currency:          resolved.Cost.Currency,
		EstimatedRun:      resolved.Cost.EstimatedRun,
		Metadata:          resolved.Metadata,
	}
	for _, cap := range resolved.Capabilities {
		record.Capabilities = append(record.Capabilities, string(cap))
	}
	settings.Worker = wcfg.Binary
	settings.WorkerArgs = wcfg.Args
	settings.Profile = record
	path, err := saveAnalysisSettings(settings)
	if err != nil {
		return err
	}

	res := profileResult{
		Configured: true,
		Worker:     Sanitize(wcfg.Binary),
		WorkerArgs: sanitizeAll(wcfg.Args),
		Profile:    sanitizeProfile(record),
		Owner:      profileOwner,
		Path:       Sanitize(path),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeProfile(res)
}

func (a *app) analysisProfileShow(args []string) error {
	c := newCmd("analysis profile show", analysisShowUsage)
	asJSON := c.fs.Bool("json", false, "emit the stored reference as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	path, err := analysisPath()
	if err != nil {
		return err
	}
	res := profileResult{
		Configured: settings.Profile != nil,
		Worker:     Sanitize(settings.Worker),
		WorkerArgs: sanitizeAll(settings.WorkerArgs),
		Profile:    sanitizeProfile(settings.Profile),
		Owner:      profileOwner,
		Path:       Sanitize(path),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeProfile(res)
}

// writeProfile renders the profile document for a terminal.
func (a *app) writeProfile(res profileResult) error {
	if !res.Configured {
		fmt.Fprint(a.stdout, "no Code analysis profile is stored\n")
		fmt.Fprint(a.stdout, "run \"babel analysis profile configure\" to obtain one from Code\n")
		return nil
	}
	rows := [][2]string{
		{"profile", res.Profile.ID},
		{"revision", strconv.Itoa(res.Profile.Revision)},
		{"owner", "Code; Babel stores this reference only (SPEC.md §2.6)"},
		{"configured", orMissing(res.Profile.ConfiguredAt)},
		{"worker", orMissing(res.Worker)},
		{"worker build", orMissing(strings.TrimSpace(res.Profile.WorkerName + " " + res.Profile.WorkerVersion))},
		{"disclosure", orMissing(res.Profile.Disclosure)},
		{"redaction", yesNo(res.Profile.RedactionRequired, "required", "not required")},
		{"capabilities", orMissing(strings.Join(res.Profile.Capabilities, " "))},
		{"settings", orMissing(res.Path)},
	}
	if res.Profile.Currency != "" {
		rows = append(rows, [2]string{"estimated run",
			fmt.Sprintf("%.4f %s", res.Profile.EstimatedRun, res.Profile.Currency)})
	}
	for _, key := range slices.Sorted(maps.Keys(res.Profile.Metadata)) {
		rows = append(rows, [2]string{"meta " + key, res.Profile.Metadata[key]})
	}
	return writeDetail(a.stdout, rows)
}

// sanitizeProfile renders every dynamic field of a stored profile. The
// metadata keys and values come from Code, which is outside Babel's trust
// boundary like any other producer (SPEC.md §3).
func sanitizeProfile(p *profileRecord) *profileRecord {
	if p == nil {
		return nil
	}
	out := *p
	out.ID = Sanitize(p.ID)
	out.WorkerName = Sanitize(p.WorkerName)
	out.WorkerVersion = Sanitize(p.WorkerVersion)
	out.Disclosure = Sanitize(p.Disclosure)
	out.Currency = Sanitize(p.Currency)
	out.Capabilities = sanitizeAll(p.Capabilities)
	if len(p.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(p.Metadata))
		for k, v := range p.Metadata {
			out.Metadata[Sanitize(k)] = Sanitize(v)
		}
	}
	return &out
}

// analysisState is the durable Phase B state one command opened: the
// hypothesis frontier, the run receipts, and the review log above them.
// They are opened together because review.Open sits on the other two, and
// closed in reverse order so the service releases its handle before the
// stores it reads do.
type analysisState struct {
	dir      string
	frontier *frontier.Store
	runs     *runstore.Store
	review   *review.Service
}

func openAnalysisState() (*analysisState, error) {
	d, err := babelDirs()
	if err != nil {
		return nil, err
	}
	dir := d.durableDir()
	front, err := frontier.Open(dir)
	if err != nil {
		return nil, err
	}
	runs, err := runstore.Open(dir)
	if err != nil {
		front.Close()
		return nil, err
	}
	svc, err := review.Open(dir, front, runs)
	if err != nil {
		runs.Close()
		front.Close()
		return nil, err
	}
	return &analysisState{dir: dir, frontier: front, runs: runs, review: svc}, nil
}

func (s *analysisState) Close() error {
	err := s.review.Close()
	if e := s.runs.Close(); err == nil {
		err = e
	}
	if e := s.frontier.Close(); err == nil {
		err = e
	}
	return err
}

// openReality opens the Reality Ledger alone. It is a separate handle on the
// same durable file rather than part of analysisState because no command
// needs both: keeping them apart means a Reality command never opens the
// frontier and an analysis command never opens the ledger.
func openReality() (*reality.Store, error) {
	d, err := babelDirs()
	if err != nil {
		return nil, err
	}
	return reality.Open(d.durableDir())
}

// recordKinds maps the identifier prefixes internal/frontier mints onto the
// record kinds review and export address.
//
// Resolving the kind from the identifier rather than from a flag is what
// keeps `babel review decide ID` and `babel export ID` honest: the operator
// pastes an ID a listing printed, and a mistyped `--type` cannot make a
// command act on the wrong table.
var recordKinds = map[string]frontier.EntityType{
	"hyp": frontier.EntityHypothesis,
	"obs": frontier.EntityObservation,
	"fnd": frontier.EntityFinding,
	"pro": frontier.EntityProposal,
}

// entityTypeFor derives a record's kind from its identifier.
func entityTypeFor(c *cmd, id string) (frontier.EntityType, error) {
	prefix, _, ok := strings.Cut(id, "_")
	if ok {
		if kind, known := recordKinds[prefix]; known {
			return kind, nil
		}
	}
	known := make([]string, 0, len(recordKinds))
	for p := range recordKinds {
		known = append(known, p+"_")
	}
	slices.Sort(known)
	return "", c.usagef("%q does not name an analysis record; identifiers start with one of %s",
		id, strings.Join(known, " "))
}

// refFor builds the reference the frontier and review services address.
func refFor(c *cmd, id string) (frontier.Ref, error) {
	kind, err := entityTypeFor(c, id)
	if err != nil {
		return frontier.Ref{}, err
	}
	return frontier.Ref{Type: kind, ID: id}, nil
}
