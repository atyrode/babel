package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The conformance suite grades a Code executable against what Babel needs
// from `code engine`, and it asks Code to implement nothing for the exam that
// it does not implement for a run: the offline half reads `--describe`, and
// the inference half is one ordinary engine job under a profile the operator
// named, whose only peculiarity is a result schema with a nonce in it.
//
// The split is the point. `--describe` never opens an interface or reaches a
// provider, so those obligations cost nothing and run anywhere. A launched
// engine is a model turn under a real profile, which is spend; it happens only
// when the caller says so, after the profile and its cost have been shown.

// ObligationResult is one obligation's verdict.
type ObligationResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// ConformanceOptions selects what the suite grades.
type ConformanceOptions struct {
	// Binary and Args launch Code exactly as a run would.
	Binary string
	Args   []string

	// Profile is the profile to describe and, with Inference, to launch. Nil
	// describes Code's default and cannot launch anything.
	Profile *ProfileRef

	// Inference authorizes the launched-engine obligations. False grades
	// the offline obligations only.
	Inference bool

	// Unsandboxed grades the launched engine against a relaxed containment
	// requirement, so "needs a sandbox" is legible as a separate finding from
	// "does not speak the protocol".
	Unsandboxed bool

	// Limits bounds the launched engine; zero fields take the defaults.
	Limits Limits
}

// ConformanceNames lists the obligations in grading order, so a report can be
// laid out before any verdict exists.
func ConformanceNames(opts ConformanceOptions) []string {
	names := []string{"describe/reports-runtime", "describe/declares-no-credential"}
	if opts.Profile != nil {
		names = append(names, "describe/resolves-profile")
	}
	if opts.Inference && opts.Profile != nil {
		names = append(names, engineObligations...)
	}
	return names
}

var engineObligations = []string{
	"engine/becomes-ready",
	"engine/declares-containment",
	"engine/registers-tools",
	"engine/submits-under-schema",
	"engine/exits-on-eof",
	"engine/reports-resources",
}

// StreamConformance grades opts, calling settled as each obligation decides,
// and returns every verdict in order.
func StreamConformance(ctx context.Context, opts ConformanceOptions, settled func(ObligationResult)) []ObligationResult {
	var results []ObligationResult
	record := func(r ObligationResult) {
		r.Passed = len(r.Failures) == 0
		results = append(results, r)
		if settled != nil {
			settled(r)
		}
	}
	client, err := New(Config{Binary: opts.Binary, Args: opts.Args, Limits: opts.Limits, Authorizer: AllowWithinGrant()})
	if err != nil {
		for _, name := range ConformanceNames(opts) {
			record(ObligationResult{Name: name, Failures: []string{err.Error()}})
		}
		return results
	}

	cfg, err := client.Configure(ctx, opts.Profile)
	runtime := ObligationResult{Name: "describe/reports-runtime"}
	credential := ObligationResult{Name: "describe/declares-no-credential"}
	switch {
	case errors.Is(err, ErrSecretDeclared):
		credential.Failures = append(credential.Failures, err.Error())
	case err != nil:
		runtime.Failures = append(runtime.Failures, err.Error())
	default:
		if cfg.Worker.Name == "" || cfg.Worker.Version == "" {
			runtime.Failures = append(runtime.Failures, "describe names no worker build")
		}
		if cfg.Privacy.Disclosure != DisclosureLocal && cfg.Privacy.Disclosure != DisclosureHosted {
			runtime.Failures = append(runtime.Failures, fmt.Sprintf("disclosure %q is neither local nor hosted", cfg.Privacy.Disclosure))
		}
		if cfg.Cost.Currency == "" {
			runtime.Failures = append(runtime.Failures, "describe quotes a cost in no currency")
		}
	}
	record(runtime)
	record(credential)
	if opts.Profile != nil {
		profile := ObligationResult{Name: "describe/resolves-profile"}
		if err != nil {
			profile.Failures = append(profile.Failures, "describe failed, so the profile could not be checked")
		} else if cfg.Profile != *opts.Profile {
			profile.Failures = append(profile.Failures, fmt.Sprintf("described %s, asked for %s", cfg.Profile, opts.Profile))
		}
		record(profile)
	}
	if !opts.Inference || opts.Profile == nil {
		return results
	}

	requirement := SandboxedRun()
	if opts.Unsandboxed {
		requirement = Unsandboxed()
	}
	client.cfg.Requirement = &requirement
	receipt, runErr := client.Run(ctx, conformanceJob(*opts.Profile))
	for _, r := range gradeEngine(receipt, runErr) {
		record(r)
	}
	return results
}

// ConformanceAnswerParam is the parameter the conformance prompt carries its
// nonce under. It is stated in the prompt's `[babel-params]` block so that the
// answer is legible to a reader of the prompt, model or fixture alike.
const ConformanceAnswerParam = "babel.conformance.answer"

// conformanceJob is the one engine job the suite runs: a result schema whose
// only field must equal a nonce minted for this run, and a prompt that says so.
// An engine that submits the nonce has read the prompt, registered the tool,
// validated a call against the schema and answered a host tool call; one
// that submits anything else has not.
func conformanceJob(profile ProfileRef) Job {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	answer := "babel-" + hex.EncodeToString(nonce[:])
	schema, _ := json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string", "const": answer},
		},
		"required":             []string{"answer"},
		"additionalProperties": false,
	})
	prompt := strings.Join([]string{
		"# Babel conformance",
		"",
		"This is a conformance check of the engine, not an analysis. Do not search, read files or reason at length.",
		"Call `" + ToolSubmit + "` exactly once with `{\"answer\": \"" + answer + "\"}` and then end your turn.",
		"",
		"[babel-params]",
		ConformanceAnswerParam + " = " + answer,
		"[end]",
		"",
	}, "\n")
	return Job{
		JobID:   "conformance/job",
		RunID:   "conformance",
		Profile: profile,
		Grant:   Grant{Disclosure: DisclosureLocal},
		Params:  map[string]string{ConformanceAnswerParam: answer},
		Output:  OutputContract{Schema: "babel.conformance-answer/1", JSONSchema: schema, Instructions: "answer with the nonce"},
		Prompt:  prompt,
		Accept: func(payload json.RawMessage) error {
			var got struct {
				Answer string `json:"answer"`
			}
			if err := json.Unmarshal(payload, &got); err != nil {
				return err
			}
			if got.Answer != answer {
				return fmt.Errorf("answer %q is not this run's nonce", got.Answer)
			}
			return nil
		},
	}
}

// gradeEngine reads the verdicts off one engine run's receipt.
func gradeEngine(receipt *Receipt, runErr error) []ObligationResult {
	fail := func(name string, failures ...string) ObligationResult {
		return ObligationResult{Name: name, Failures: failures}
	}
	if receipt == nil {
		var out []ObligationResult
		for _, name := range engineObligations {
			out = append(out, fail(name, "the engine could not be launched: "+runErr.Error()))
		}
		return out
	}
	ready := fail("engine/becomes-ready")
	for _, sentinel := range []error{ErrHandshakeTimeout, ErrProtocolMismatch, ErrRuntimeInfo} {
		if errors.Is(runErr, sentinel) {
			ready.Failures = append(ready.Failures, runErr.Error())
		}
	}

	containment := fail("engine/declares-containment")
	if errors.Is(runErr, ErrContainment) || errors.Is(runErr, ErrPlatformUnqualified) {
		containment.Failures = append(containment.Failures, runErr.Error())
	} else if receipt.Containment.Backend == "" && len(ready.Failures) == 0 {
		containment.Failures = append(containment.Failures, "no containment was recorded")
	}

	tools := fail("engine/registers-tools")
	if !containsString(receipt.Tools, ToolSubmit) {
		tools.Failures = append(tools.Failures, fmt.Sprintf("the engine confirmed %v, which lacks %s", receipt.Tools, ToolSubmit))
	}

	submits := fail("engine/submits-under-schema")
	switch {
	case receipt.Result == nil:
		submits.Failures = append(submits.Failures, fmt.Sprintf("no accepted submission after %d attempt(s)", receipt.Submissions))
		if runErr != nil {
			submits.Failures = append(submits.Failures, runErr.Error())
		}
	case receipt.Submissions != 1:
		submits.Failures = append(submits.Failures, fmt.Sprintf("%d submissions, asked for exactly one", receipt.Submissions))
	}

	exits := fail("engine/exits-on-eof")
	switch {
	case errors.Is(runErr, ErrWorkerLingered):
		exits.Failures = append(exits.Failures, runErr.Error())
	case errors.Is(runErr, ErrDirtyExit):
		exits.Failures = append(exits.Failures, runErr.Error())
	case receipt.ExitCode != 0 && receipt.Result != nil:
		exits.Failures = append(exits.Failures, fmt.Sprintf("exit status %d after a complete run", receipt.ExitCode))
	}

	resources := fail("engine/reports-resources")
	if receipt.Resources == nil {
		resources.Failures = append(resources.Failures, "Code wrote no finished report with measurements")
	} else if receipt.Resources.CPUSeconds == nil && receipt.Resources.MaxRSSBytes == nil {
		resources.Failures = append(resources.Failures, "the finished report measured nothing")
	}
	if receipt.Duration <= 0 {
		resources.Failures = append(resources.Failures, "the receipt records no duration")
	}
	return []ObligationResult{ready, containment, tools, submits, exits, resources}
}
