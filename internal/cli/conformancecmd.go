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
with the run-scoped broker credential. One obligation per line; the exit
code is 0 only when every obligation held.

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

	results := worker.RunConformanceWith(ctx, worker.ConformanceOptions{
		Worker:      binary,
		Args:        workerArgs,
		Unsandboxed: *unsandboxed,
	})
	res := conformanceResult{
		Worker:      Sanitize(binary),
		WorkerArgs:  sanitizeAll(workerArgs),
		Unsandboxed: *unsandboxed,
		Total:       len(results),
		Obligations: make([]obligationRow, 0, len(results)),
	}
	for _, r := range results {
		if r.Passed {
			res.Passed++
		} else {
			res.Failed++
		}
		res.Obligations = append(res.Obligations, obligationRow{
			Name:     Sanitize(r.Name),
			Passed:   r.Passed,
			Failures: sanitizeAll(r.Failures),
		})
	}
	res.OK = res.Failed == 0

	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		for _, row := range res.Obligations {
			fmt.Fprintf(a.stdout, "%-6s%s\n", yesNo(row.Passed, "ok", "FAIL"), row.Name)
			for _, failure := range row.Failures {
				fmt.Fprintf(a.stdout, "        %s\n", failure)
			}
		}
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
