package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/atyrode/babel/internal/worker"
)

const conformanceUsage = `Usage: babel conformance WORKER [--worker-arg ARG]... [flags]

Runs the babel.analysis-worker contract suite against the executable at
WORKER: the handshake, the resolved configuration, the grant boundary, tool
decisions, terminal events, cancellation, and the worker's own discipline
with the run-scoped broker credential. One obligation per line, printed the
moment that obligation settles; the exit code is 0 only when every
obligation held.

Obligations are graded one at a time, and one that cannot reach the worker
spends its whole handshake budget — 15 seconds — before its verdict is
known, so grading a program that is not a worker takes that long per
obligation. The line for the last obligation to settle is therefore also
the name of the obligation being graded now: a suite that appears to have
stopped has stopped somewhere legible. --json is exempt: a machine-readable
report is one document, written after the last obligation.

The credential obligation deliberately instructs the worker to leak: it is
told to echo its broker token, and it holds only if the run still reached a
terminal result, the token appears in nothing the worker wrote, and the
token appears nowhere in the receipt. All three, because a worker that
emits no bytes at all would otherwise pass a test for an absence.

The same suite is what Babel's own tests run against their fake worker, so
an implementation that passes here is one Babel can supervise. It is a
command rather than an importable package because Go forbids importing
internal/ from another repository, and the counterpart is developed in one
(SPEC.md §2.6).

A worker need not speak the protocol at argv[0]: Code is an interactive
program that speaks it under a subcommand, so --worker-arg is how the
executable is put into worker mode, exactly as for "babel explore".

Nothing is analysed, no session is read, and no credential is needed: the
suite drives the worker with a synthetic job over its own pipes.

A worker that declares honestly weak containment fails every obligation
that reaches worker mode with the same containment error, which says
nothing about whether it implements the rest of the protocol.
--unsandboxed grades it against a relaxed requirement so "needs a sandbox"
is legible as a separate finding from "does not speak the protocol". It
never relaxes anything about a real run: which containment an exploration
demands is decided at launch, not here.

Flags:
  --worker-arg ARG   extra argument for the worker executable; repeatable
  --unsandboxed      grade against relaxed containment, not the strict default
  --json             emit the report as JSON on stdout
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
	// Unsandboxed records that the grading was relaxed. It is always present
	// rather than omitted when false, because a relaxed pass reported
	// identically to a strict one would be the most misleading output this
	// command could produce.
	Unsandboxed bool            `json:"unsandboxed"`
	OK          bool            `json:"ok"`
	Total       int             `json:"total"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	Obligations []obligationRow `json:"obligations"`
}

// conformanceCmd serves `babel conformance WORKER`.
func (a *app) conformanceCmd(ctx context.Context, args []string) error {
	c := newCmd("conformance", conformanceUsage)
	var workerArgs repeatedFlag
	c.fs.Var(&workerArgs, "worker-arg", "extra argument for the worker executable; repeatable")
	unsandboxed := c.fs.Bool("unsandboxed", false, "grade against relaxed containment")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	positional := c.args()
	if len(positional) != 1 {
		return c.usagef("conformance takes exactly one worker executable, got %d", len(positional))
	}
	// A worker that cannot be launched is a rejected invocation, not a
	// failed contract: reporting eleven identical spawn failures would say
	// nothing about the implementation.
	binary, err := resolveWorkerBinary(c, positional[0])
	if err != nil {
		return err
	}

	res := conformanceResult{
		Worker:      Sanitize(binary),
		WorkerArgs:  sanitizeAll(workerArgs),
		Unsandboxed: *unsandboxed,
	}
	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		return worker.StreamConformance(ctx, worker.ConformanceOptions{
			Worker:      binary,
			Args:        workerArgs,
			Unsandboxed: *unsandboxed,
		}, settled)
	}
	return a.reportConformance(res, *asJSON, grade)
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
	a.diagf("%d of %d %s failed; the worker does not yet implement the babel.analysis-worker contract\n",
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
