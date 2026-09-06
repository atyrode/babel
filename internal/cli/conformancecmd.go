package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/atyrode/babel/internal/worker"
)

const conformanceUsage = `Usage: babel conformance CODE [--worker-arg ARG]... [flags]

Checks the Code executable at CODE against what "babel explore" needs from
"code engine". One obligation per line, printed the moment that obligation
settles; the exit code is 0 only when every obligation held.

Without --allow-inference nothing is launched. The suite runs
"code engine --describe", which resolves a profile and reports its non-secret
metadata without opening an interface or reaching a provider, and grades what
it reports: a worker build, a disclosure class, a cost in a currency, no
credential-shaped metadata, and — with --profile — the profile that was asked
for.

With --allow-inference and --profile, the suite also runs one real engine job
under that profile: a prompt asking the model to record a nonce through the
result tool, under a schema that admits nothing else. That is one model turn
and it costs what the profile costs; the profile and its cost estimate are
printed before the launch. The obligations grade the launch itself — the
ready frame and Code's runtime-info, the containment it declares, the tools
the engine confirms, the submission the schema admits, the exit on stdin
close, and the measurements Code reports afterwards. --unsandboxed grades the
declared containment against a relaxed requirement so "needs a sandbox" is
legible as a separate finding from "does not speak the protocol"; it never
relaxes anything about a real run.

Nothing about the exam is Code's to implement: the same "code engine" surface
"babel explore" uses is what is graded, and the only thing peculiar to the
exam is the schema the one job carries.

Flags:
  --worker-arg ARG     extra argument for the Code executable; repeatable
  --profile ID[@REV]   the profile to describe and, with --allow-inference, to launch
  --allow-inference    launch one engine job under --profile; this spends
  --unsandboxed        grade declared containment against the relaxed requirement
  --json               emit the report as JSON on stdout
`

// obligationRow is one obligation's verdict in machine-readable output.
type obligationRow struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

type conformanceResult struct {
	Worker     string   `json:"worker"`
	WorkerArgs []string `json:"worker_args,omitempty"`
	Profile    string   `json:"profile,omitempty"`
	// Inference records that an engine was launched, and Unsandboxed that
	// the grading was relaxed. Both are always present rather than omitted
	// when false: an offline pass reported identically to a launched one,
	// or a relaxed pass identically to a strict one, would be the most
	// misleading output this command could produce.
	Inference   bool            `json:"inference"`
	Unsandboxed bool            `json:"unsandboxed"`
	OK          bool            `json:"ok"`
	Total       int             `json:"total"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	Obligations []obligationRow `json:"obligations"`
}

// conformanceCmd serves `babel conformance CODE`.
func (a *app) conformanceCmd(ctx context.Context, args []string) error {
	c := newCmd("conformance", conformanceUsage)
	var workerArgs repeatedFlag
	c.fs.Var(&workerArgs, "worker-arg", "extra argument for the Code executable; repeatable")
	profileFlag := c.fs.String("profile", "", "the profile to describe and, with --allow-inference, to launch")
	inference := c.fs.Bool("allow-inference", false, "launch one engine job under --profile")
	unsandboxed := c.fs.Bool("unsandboxed", false, "grade against relaxed containment")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	positional := c.args()
	if len(positional) != 1 {
		return c.usagef("conformance takes exactly one Code executable, got %d", len(positional))
	}
	// An executable that cannot be launched is a rejected invocation, not a
	// failed contract: reporting identical spawn failures for every
	// obligation would say nothing about the implementation.
	binary, err := resolveWorkerBinary(c, positional[0])
	if err != nil {
		return err
	}
	var profile *worker.ProfileRef
	if *profileFlag != "" {
		ref, err := parseProfileRef(*profileFlag)
		if err != nil {
			return c.usagef("--profile: %v", err)
		}
		profile = &ref
	}
	if *inference && profile == nil {
		return c.usagef("--allow-inference launches an engine and needs --profile to say which")
	}

	opts := worker.ConformanceOptions{
		Binary:      binary,
		Args:        workerArgs,
		Profile:     profile,
		Inference:   *inference,
		Unsandboxed: *unsandboxed,
	}
	res := conformanceResult{
		Worker:      Sanitize(binary),
		WorkerArgs:  sanitizeAll(workerArgs),
		Inference:   *inference,
		Unsandboxed: *unsandboxed,
	}
	if profile != nil {
		res.Profile = profile.String()
	}
	if *inference {
		// The spend is disclosed before it happens, from the same describe
		// the suite is about to grade. A profile Code cannot describe is
		// not launched: the offline obligations will say why.
		if err := a.discloseInference(ctx, binary, workerArgs, *profile); err != nil {
			return err
		}
	}
	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		return worker.StreamConformance(ctx, opts, settled)
	}
	return a.reportConformance(res, *asJSON, grade)
}

// discloseInference prints what an engine launch will cost before the suite
// launches it. It refuses when the profile cannot be described, because a
// launch whose cost is unknown is a launch nobody authorized.
func (a *app) discloseInference(ctx context.Context, binary string, args []string, profile worker.ProfileRef) error {
	client, err := worker.New(worker.Config{Binary: binary, Args: args})
	if err != nil {
		return err
	}
	cfg, err := client.Configure(ctx, &profile)
	if err != nil {
		a.diagf("refusing to launch an engine: profile %s could not be described: %s\n",
			Sanitize(profile.String()), Sanitize(err.Error()))
		return errReported
	}
	a.diagf("launching one engine job under profile %s (%s, disclosure %s, estimated %.4f %s per run)\n",
		Sanitize(cfg.Profile.String()), Sanitize(cfg.Metadata["model"]), Sanitize(cfg.Privacy.Disclosure),
		cfg.Cost.EstimatedRun, Sanitize(cfg.Cost.Currency))
	return nil
}

// reportConformance grades a worker through grade and reports the verdicts,
// with res already carrying what is known about the examination before it
// starts.
//
// The human report is written as the run proceeds: an obligation's line goes
// out the moment that obligation settles, so the last line on the terminal
// names the last thing decided and, by omission, the obligation the suite is
// working on now. It matters because an obligation that cannot reach the worker
// spends its whole handshake budget before failing, and a suite of those in a
// row is minutes during which a report held back until the end is
// indistinguishable from a hung command.
//
// JSON is one document written once at the end, because that is what a --json
// invocation promises: a stream of partial documents would not be parseable,
// and the progress this gives a human is not what a program consuming the
// report needs.
//
// grade receives the per-verdict callback rather than handing this function a
// finished report, so the streaming path is the one a test can drive with
// obligations whose settling it controls.
func (a *app) reportConformance(res conformanceResult, asJSON bool, grade func(settled func(worker.ObligationResult)) []worker.ObligationResult) error {
	var settled func(worker.ObligationResult)
	if !asJSON {
		settled = func(result worker.ObligationResult) { a.printObligation(obligationRowOf(result)) }
	}
	results := grade(settled)

	res.Total = len(results)
	res.Obligations = make([]obligationRow, 0, len(results))
	for _, r := range results {
		if r.Passed {
			res.Passed++
		} else {
			res.Failed++
		}
		res.Obligations = append(res.Obligations, obligationRowOf(r))
	}
	res.OK = res.Failed == 0

	if asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.stdout, "\n%d %s, %d passed, %d failed\n",
			res.Total, plural(res.Total, "obligation", "obligations"), res.Passed, res.Failed)
		if res.Unsandboxed {
			fmt.Fprintf(a.stdout, "graded against relaxed containment; a real run demands the strict requirement\n")
		}
	}
	if res.OK {
		return nil
	}
	// The report is the result document and it is already on stdout; the
	// exit code is what an exam is for, so the failure gets a pointer to
	// the contract rather than a second recital of it.
	a.diagf("%d of %d %s failed; this Code does not yet provide what babel explore needs from code engine\n",
		res.Failed, res.Total, plural(res.Total, "obligation", "obligations"))
	return errReported
}

// obligationRowOf renders one verdict for output. Every string a worker
// influenced reaches a terminal through Sanitize: an obligation's name is
// Babel's own, but its failure messages quote what the worker said.
func obligationRowOf(result worker.ObligationResult) obligationRow {
	return obligationRow{
		Name:     Sanitize(result.Name),
		Passed:   result.Passed,
		Failures: sanitizeAll(result.Failures),
	}
}

// printObligation writes one obligation's verdict and, if it failed, the
// messages that decided it.
func (a *app) printObligation(row obligationRow) {
	fmt.Fprintf(a.stdout, "%-6s%s\n", yesNo(row.Passed, "ok", "FAIL"), row.Name)
	for _, failure := range row.Failures {
		fmt.Fprintf(a.stdout, "        %s\n", failure)
	}
}

// resolveWorkerBinary fixes which executable the suite will launch, before it
// launches it. A bare name is resolved through PATH exactly as the client
// would resolve it, so the report names the file that actually ran.
func resolveWorkerBinary(c *cmd, path string) (string, error) {
	binary, err := exec.LookPath(path)
	if err == nil {
		return binary, nil
	}
	// LookPath reports a directory as a permission problem, which sends an
	// operator looking for the wrong remedy.
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return "", c.usagef("worker %s is a directory, not an executable", path)
	}
	var lookup *exec.Error
	if errors.As(err, &lookup) {
		err = lookup.Err
	}
	return "", c.usagef("worker %s: %v", path, err)
}
