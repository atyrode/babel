package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// Transport and supervision defaults. They are conservative on purpose: an
// analysis run is minutes of model work, so the budgets that must be small are
// the ones bounding *silence* and *shutdown*, not the ones bounding work.
const (
	defaultHandshakeTimeout = 30 * time.Second
	defaultIdleTimeout      = 5 * time.Minute
	defaultExitGrace        = 10 * time.Second
	defaultTerminateGrace   = 2 * time.Second
	defaultDrainGrace       = 2 * time.Second
	defaultMaxFrameBytes    = 1 << 20
	defaultMaxReassembled   = 64 << 20
	defaultMaxEvents        = 100_000
	defaultMaxToolRequests  = 1024
	defaultMaxProgress      = 256
	defaultStderrTailBytes  = 4 << 10

	// readBufferSize is the stdout read buffer. Lines are usually short; the
	// oversize check is enforced against Limits.MaxFrameBytes independently
	// of this, so the buffer is a throughput choice and not a protocol one.
	readBufferSize = 64 << 10

	// stderrLineLimit bounds one retained diagnostic line. Stderr is not
	// protocol, so an over-long line is truncated rather than fatal — but it
	// is bounded, because buffering a runaway log line would let the engine
	// exhaust Babel's memory.
	stderrLineLimit = 8 << 10

	// toolBudgetSlack is how many over-budget tool calls the engine may make
	// before Babel gives up on it. Every one of them is denied with
	// DenyLimit, so an engine that keeps asking is looping rather than
	// adapting, and a run that cannot progress must end rather than spin.
	toolBudgetSlack = 16

	// maxArgumentDigestBytes bounds how much of a tool argument blob is read
	// to digest it. Arguments are never stored, only fingerprinted.
	maxArgumentDigestBytes = 1 << 20

	// engineSubcommand and its flags are Code's `engine` surface. Babel
	// composes argv from them; an operator's stored worker arguments precede
	// them and name only the executable's own mode.
	engineSubcommand  = "engine"
	flagProfile       = "--profile"
	flagRuntimeInfo   = "--runtime-info"
	flagDescribe      = "--describe"
	runtimeInfoFile   = "runtime.json"
	runtimeInfoPrefix = "babel-engine-"
)

// Decision is one authorization outcome from the injected policy. Reason is
// recorded in the receipt and sent to the model, so it must explain the
// decision without disclosing anything the model is not cleared to see.
//
// Results is the evidence a facility served, and it is the one field of a
// Decision that never reaches the receipt. The asymmetry is the §9 boundary:
// the pipe carries content to the model because a model that cannot read a
// record cannot form an observation about it, and the receipt carries locators
// and digests only because a plaintext store of archive content readable by
// anyone with catalog access is exactly what §9 forbids.
//
// It is raw JSON rather than a Go type because the shape belongs to the
// facility behind the capability: internal/explore decides what a corpus-search
// hit is, and a type here would be Babel's control plane asserting a schema
// over evidence it does not own. It travels to the model as the text of the
// tool result, byte for byte.
type Decision struct {
	Allow   bool
	Reason  string
	Results json.RawMessage
}

// ToolRequest is one engine call to an evidence or execution tool, as handed
// to the policy. Arguments are the model's JSON as the engine validated them
// against the tool's schema: the policy sees them, the receipt never does.
type ToolRequest struct {
	JobID      string
	RunID      string
	Index      int
	RequestID  string
	ToolCallID string
	Capability Capability
	Tool       string
	Arguments  json.RawMessage
	Grant      Grant
}

// Authorizer decides tool calls. Babel authorizes every one of them (SPEC.md
// §6.5), and the tool's capability was checked against the grant before the
// job launched, so an Authorizer can only narrow what a run may do.
type Authorizer interface {
	Authorize(ctx context.Context, req ToolRequest) Decision
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, req ToolRequest) Decision

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, req ToolRequest) Decision {
	return f(ctx, req)
}

// AllowWithinGrant is the permissive policy: it allows every call to a tool
// the job registered, held to the names Babel serves for the capability. It
// is not "allow everything" — the registration check ran before launch and is
// not bypassable — but it answers with no evidence, so it belongs in
// development and offline conformance rather than in a run whose scope was
// negotiated with an operator.
func AllowWithinGrant() Authorizer {
	return AuthorizerFunc(func(_ context.Context, req ToolRequest) Decision {
		if !ServesTool(req.Capability, req.Tool) {
			return DenyUnservedTool(req.Capability, req.Tool)
		}
		return Decision{Allow: true, Reason: "within grant"}
	})
}

// DenyAll refuses every call with the given reason. It is the default when no
// Authorizer is configured: a run with no policy is not a run with a
// permissive policy.
func DenyAll(reason string) Authorizer {
	return AuthorizerFunc(func(context.Context, ToolRequest) Decision {
		return Decision{Allow: false, Reason: reason}
	})
}

// Limits bounds the transport and the shutdown, not the analysis. Zero fields
// select the documented default.
type Limits struct {
	// HandshakeTimeout bounds the wait for the engine's ready frame. It
	// covers Code resolving the profile and establishing the sandbox.
	HandshakeTimeout time.Duration

	// IdleTimeout bounds the gap between frames. Stderr output does not
	// reset it: an engine that talks only on stderr is stalled, and the
	// whole point of the timer is to notice that.
	IdleTimeout time.Duration

	// ExitGrace bounds how long the process tree may take to exit after
	// Babel closes its stdin before the tree is killed (ErrWorkerLingered).
	ExitGrace time.Duration

	// TerminateGrace is how long SIGTERM is given before SIGKILL when Babel
	// tears the tree down.
	TerminateGrace time.Duration

	// DrainGrace bounds how long Babel keeps reading stdout after the child
	// has exited. A grandchild holding the pipe open would otherwise keep the
	// stream from ever reaching EOF.
	DrainGrace time.Duration

	// MaxFrameBytes is the largest physical stdout line accepted
	// (ErrOversizedFrame). It matches the engine's own physical bound.
	MaxFrameBytes int

	// MaxReassembledBytes bounds one v2 chunk sequence (ErrOversizedFrame).
	MaxReassembledBytes int

	// MaxEvents bounds the whole stream (ErrEventBudget).
	MaxEvents int

	// MaxToolRequests bounds authorized calls; further ones are denied with
	// DenyLimit.
	MaxToolRequests int

	// MaxProgressRecords bounds how many lifecycle events a receipt keeps. A
	// chatty engine must not make the audit record unbounded, so the excess
	// is counted instead of stored.
	MaxProgressRecords int

	// StderrTailBytes bounds the retained tail of diagnostics.
	StderrTailBytes int
}

// withDefaults fills in the zero fields.
func (l Limits) withDefaults() Limits {
	if l.HandshakeTimeout <= 0 {
		l.HandshakeTimeout = defaultHandshakeTimeout
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = defaultIdleTimeout
	}
	if l.ExitGrace <= 0 {
		l.ExitGrace = defaultExitGrace
	}
	if l.TerminateGrace <= 0 {
		l.TerminateGrace = defaultTerminateGrace
	}
	if l.DrainGrace <= 0 {
		l.DrainGrace = defaultDrainGrace
	}
	if l.MaxFrameBytes <= 0 {
		l.MaxFrameBytes = defaultMaxFrameBytes
	}
	if l.MaxReassembledBytes <= 0 {
		l.MaxReassembledBytes = defaultMaxReassembled
	}
	if l.MaxEvents <= 0 {
		l.MaxEvents = defaultMaxEvents
	}
	if l.MaxToolRequests <= 0 {
		l.MaxToolRequests = defaultMaxToolRequests
	}
	if l.MaxProgressRecords <= 0 {
		l.MaxProgressRecords = defaultMaxProgress
	}
	if l.StderrTailBytes <= 0 {
		l.StderrTailBytes = defaultStderrTailBytes
	}
	return l
}

// Config describes how to launch and supervise Code's engine.
type Config struct {
	// Binary is the Code executable. Required.
	Binary string

	// Args are the operator's own arguments, placed before the `engine`
	// subcommand Babel appends. They must carry no secrets: argv is visible
	// in any process listing.
	Args []string

	// Dir is the child's working directory. Empty means the parent's.
	Dir string

	// Env is appended to the derived launch environment: standard paths,
	// Code's profile/executable overrides, and user-session transport.
	// It must carry no credentials, for the same reason Args must not.
	Env []string

	// Authorizer decides evidence tool calls. Nil fails closed: every call
	// is denied.
	Authorizer Authorizer

	// Limits bounds the transport and the shutdown.
	Limits Limits

	// Diagnostics receives the engine's stderr, one line at a time. Nil
	// discards it. The bounded tail is recorded in the receipt regardless.
	Diagnostics io.Writer

	// Requirement is the containment Code must declare. The zero value means
	// SandboxedRun: the strict setting is the default deliberately, because
	// a permissive default would quietly become the norm and the operator
	// who wants to relax it should have to say so per run. Set Unsandboxed
	// for a run that genuinely needs no boundary.
	Requirement *Requirement

	// OnProgress is called for each lifecycle event as it arrives, so a
	// caller's interface stays responsive while a run is in flight (SPEC.md
	// §2.6). It runs on the supervision goroutine and must not block: a slow
	// callback delays the next tool authorization.
	OnProgress func(ProgressRecord)
}

// Client supervises engine processes. One Client may run many jobs; each Run
// or Configure launches, supervises and reaps its own process.
type Client struct {
	cfg Config
}

// New validates cfg and returns a Client. It performs no I/O: the binary is
// resolved when a process is launched, so New never blocks and never reports
// whether Code is installed.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("worker: binary is required")
	}
	return &Client{cfg: cfg}, nil
}

// authorizer is the configured policy, failing closed when absent.
func (c *Client) authorizer() Authorizer {
	if c.cfg.Authorizer == nil {
		return DenyAll("no authorizer configured")
	}
	return c.cfg.Authorizer
}

// requirement resolves the containment the run demands. A nil Config field
// means the strict default rather than none: the failure mode of the opposite
// choice is a run that silently executes outside a sandbox because a caller
// forgot a field.
func (c *Client) requirement() Requirement {
	if c.cfg.Requirement != nil {
		return *c.cfg.Requirement
	}
	return SandboxedRun()
}

// env preserves the launch configuration Code needs to resolve the same
// profile its configuration ceremony saved and to reach the user's systemd
// manager. Provider credentials and model-selection variables are not inherited.
func (c *Client) env() []string {
	inherited := [...]string{
		"HOME", "PATH", "TMPDIR", "LANG",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
		"CODE_PROFILE_STATE", "CODE_OMP",
	}
	env := make([]string, 0, len(inherited)+len(c.cfg.Env))
	for _, key := range inherited {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, c.cfg.Env...)
}

// argv composes the executable's arguments: the operator's own, then the
// engine subcommand and Babel's flags.
func (c *Client) argv(engine ...string) []string {
	args := make([]string, 0, len(c.cfg.Args)+1+len(engine))
	args = append(args, c.cfg.Args...)
	args = append(args, engineSubcommand)
	return append(args, engine...)
}

// Configure asks Code to describe a profile without launching anything: it
// runs `engine --describe`, which resolves the profile and reports its
// reference plus non-secret privacy, cost and provider metadata, and never
// opens an interface or reaches a provider (SPEC.md §2.6). A nil profile
// describes Code's default.
//
// Babel persists only what this returns. A profile that declares
// credential-shaped metadata fails with ErrSecretDeclared rather than having
// one value redacted: a worker that put a secret there once will do it again.
func (c *Client) Configure(ctx context.Context, profile *ProfileRef) (*Configuration, error) {
	engine := []string{flagDescribe}
	if profile != nil {
		engine = append(engine, flagProfile, profile.String())
	}
	limits := c.cfg.Limits.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, limits.HandshakeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cfg.Binary, c.argv(engine...)...)
	cmd.Dir = c.cfg.Dir
	cmd.Env = c.env()
	var stdout bytes.Buffer
	stderr := &tail{limit: limits.StderrTailBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%w: describe exited %d: %s", ErrDirtyExit, exit.ExitCode(), stderr)
		}
		return nil, fmt.Errorf("worker: describe: %w", err)
	}
	info, err := decodeRuntimeInfo(bytes.TrimSpace(stdout.Bytes()))
	if err != nil {
		return nil, err
	}
	if err := validateMetadata(info.Metadata); err != nil {
		return nil, err
	}
	if profile != nil && info.Profile != *profile {
		return nil, fmt.Errorf("%w: asked for %s, Code described %s", ErrProfileMismatch, profile, info.Profile)
	}
	return info.configurationOf(), nil
}

// Run executes one analysis job and returns its receipt.
//
// Babel owns the whole boundary here (SPEC.md §2.6): the launch, the
// containment check before any prompt is written, authorization of every tool
// call, cancellation, the lifetime of the entire process tree, and the final
// status. Analysis is never detached — Run returns only after the tree is
// reaped and every reader goroutine has finished.
//
// A receipt is returned whenever the process started, including on failure:
// the receipt is the audit record of what happened, and a failed run is
// exactly when it is needed. It never contains a credential.
func (c *Client) Run(ctx context.Context, job Job) (*Receipt, error) {
	if err := job.validate(); err != nil {
		return nil, err
	}
	limits := c.cfg.Limits.withDefaults()

	// The sidecar's directory is private to this launch: created 0700,
	// named unguessably, and removed with the receipt captured. Nothing else
	// reads it, so nothing else can read the launch facts of a run that is
	// not its own.
	dir, err := os.MkdirTemp("", runtimeInfoPrefix)
	if err != nil {
		return nil, fmt.Errorf("worker: runtime-info directory: %w", err)
	}
	defer os.RemoveAll(dir)
	runtimePath := filepath.Join(dir, runtimeInfoFile)

	s, err := c.start(ctx, limits, c.argv(flagProfile, job.Profile.String(), flagRuntimeInfo, runtimePath))
	if err != nil {
		return nil, err
	}

	r := &runner{
		client:      c,
		session:     s,
		job:         job,
		limits:      limits,
		requirement: c.requirement(),
		runtimePath: runtimePath,
		unknown:     make(map[string]struct{}),
		receipt: &Receipt{
			JobID:     job.JobID,
			RunID:     job.RunID,
			Profile:   job.Profile,
			Recipes:   job.Recipes,
			Sources:   job.Sources,
			Grant:     job.Grant,
			ExitCode:  -1,
			StartedAt: time.Now().UTC(),
		},
	}

	err = r.execute(ctx)
	r.receipt.FinishedAt = time.Now().UTC()
	r.receipt.Duration = r.receipt.FinishedAt.Sub(r.receipt.StartedAt)
	r.receipt.ExitCode = s.exitCode()
	r.receipt.StderrTail = s.tail.String()
	r.receipt.UnknownFrames = sortedKeys(r.unknown)
	r.readFinishedReport()
	if err != nil && r.receipt.Failure == nil {
		r.receipt.Failure = &FailureRecord{
			Origin:  failureOrigin(err),
			Code:    failureCode(err),
			Message: err.Error(),
			At:      r.receipt.FinishedAt,
		}
	}
	return r.receipt, err
}

// runner holds the per-run supervision state. It is single-goroutine: only
// the supervision loop mutates it.
type runner struct {
	client  *Client
	session *session
	job     Job
	limits  Limits
	receipt *Receipt
	ids     commandIDs

	// requirement is the containment the run demands of Code. Babel does
	// not implement the sandbox (decision 53), so this is the boundary it
	// can still refuse to proceed without.
	requirement Requirement
	runtimePath string

	events    int
	toolCount int
	progress  int
	ended     bool
	// invoked records whether the prompt reached the model at all. A prompt
	// the engine completed locally never produces an agent_end.
	invoked  bool
	unknown  map[string]struct{}
	finished *RuntimeInfo
}

// execute performs the launch, the containment check, the registration and
// the prompt, and supervises the stream. Teardown always runs; whether it
// kills the tree depends on whether anything in it is still alive when the
// stream ends.
func (r *runner) execute(ctx context.Context) error {
	s := r.session

	if err := r.ready(ctx); err != nil {
		s.teardown(true)
		return err
	}
	if err := r.admit(); err != nil {
		// Nothing is owed to a launch Babel refuses, and the prompt is what
		// is not owed. Stdin closes with no command written; the engine
		// disposes on EOF, and the grace is what makes that observable.
		return errors.Join(err, s.refuse(r.limits.ExitGrace))
	}
	if err := r.register(ctx); err != nil {
		s.teardown(true)
		return err
	}
	if err := r.prompt(ctx); err != nil {
		s.teardown(true)
		return err
	}

	fatal := r.loop(ctx)
	if fatal == nil {
		r.stats(ctx)
	}
	return r.finish(ctx, fatal)
}

// ready waits for the engine's ready frame and checks that it offers the
// transport Babel speaks.
func (r *runner) ready(ctx context.Context) error {
	s := r.session
	in, err := s.next(ctx, r.limits.HandshakeTimeout)
	if err != nil {
		if errors.Is(err, errTimeout) {
			return fmt.Errorf("%w: no ready frame within %s", ErrHandshakeTimeout, r.limits.HandshakeTimeout)
		}
		return err
	}
	if in.err != nil {
		if errors.Is(in.err, io.EOF) {
			return fmt.Errorf("%w: engine closed its stdout before a ready frame, exit status %d",
				ErrHandshakeTimeout, s.exitCode())
		}
		return in.err
	}
	f := in.frame
	if f.Type != frameReady {
		return fmt.Errorf("%w: first frame was %q, not ready", ErrProtocolMismatch, f.Type)
	}
	offersV2 := false
	for _, v := range f.SupportedVersions {
		offersV2 = offersV2 || v == rpcProtocolVersion
	}
	if !offersV2 {
		return fmt.Errorf("%w: engine offers RPC versions %v, Babel needs %d",
			ErrProtocolMismatch, f.SupportedVersions, rpcProtocolVersion)
	}
	return nil
}

// admit reads Code's launch report and decides whether this engine may be
// prompted at all. Every refusal here happens before a byte of the prompt is
// written: what a refused engine has seen is the profile it was launched
// under.
func (r *runner) admit() error {
	info, err := readRuntimeInfo(r.runtimePath)
	if err != nil {
		return err
	}
	if err := validateMetadata(info.Metadata); err != nil {
		return err
	}
	if info.Profile != r.job.Profile {
		return fmt.Errorf("%w: job named %s, Code launched %s", ErrProfileMismatch, r.job.Profile, info.Profile)
	}
	r.receipt.Worker = info.Worker
	r.receipt.Privacy = info.Privacy
	r.receipt.Cost = info.Cost
	r.receipt.Metadata = info.Metadata
	if info.Containment == nil {
		return fmt.Errorf("%w: Code declared no containment for the launch", ErrContainment)
	}
	r.receipt.Containment = *info.Containment
	return info.Containment.Satisfies(r.requirement)
}

// register negotiates the transport and registers the job's tools.
func (r *runner) register(ctx context.Context) error {
	if _, err := r.call(ctx, negotiateCommand{ID: r.ids.next(), Type: commandNegotiate, ProtocolVersion: rpcProtocolVersion}); err != nil {
		return err
	}
	tools := make([]hostToolOnWire, 0, len(r.job.Tools)+1)
	for _, tool := range r.job.Tools {
		tools = append(tools, hostToolOnWire{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			LoadMode:    tool.LoadMode,
		})
	}
	tools = append(tools, hostToolOnWire{
		Name:        ToolSubmit,
		Description: submitDescription,
		Parameters:  r.job.Output.JSONSchema,
		LoadMode:    "essential",
	})
	data, err := r.call(ctx, setHostToolsCommand{ID: r.ids.next(), Type: commandSetHostTools, Tools: tools})
	if err != nil {
		return err
	}
	var confirmed struct {
		ToolNames []string `json:"toolNames"`
	}
	if err := json.Unmarshal(data, &confirmed); err != nil {
		return fmt.Errorf("%w: set_host_tools answered with %s", ErrMalformedFrame, strings.TrimSpace(string(data)))
	}
	for _, tool := range tools {
		if !containsString(confirmed.ToolNames, tool.Name) {
			return fmt.Errorf("%w: engine registered %v, not %q", ErrCommandFailed, confirmed.ToolNames, tool.Name)
		}
	}
	r.receipt.Tools = confirmed.ToolNames
	return nil
}

// submitDescription is what the model reads about the submit tool. The job's
// own instructions say what the result means; this says what calling it does.
const submitDescription = "Record this job's result. The arguments are the complete result as it stands; " +
	"calling again replaces the earlier submission, so include everything to be kept. " +
	"A rejected submission leaves the previous accepted one in place and explains what to fix."

// prompt writes the job's prompt. It is the first moment the run's material
// leaves Babel.
func (r *runner) prompt(ctx context.Context) error {
	data, err := r.call(ctx, promptCommand{ID: r.ids.next(), Type: commandPrompt, Message: r.job.Prompt})
	if err != nil {
		return err
	}
	var ack struct {
		AgentInvoked *bool `json:"agentInvoked"`
	}
	if len(data) > 0 && json.Unmarshal(data, &ack) == nil && ack.AgentInvoked != nil && !*ack.AgentInvoked {
		// The engine completed the prompt without a model turn. There is
		// nothing to supervise and nothing was submitted.
		r.ended = true
		return nil
	}
	r.invoked = true
	return nil
}

// call writes one command and waits for its response, handling every other
// frame that arrives first. It returns the response's data, or
// ErrCommandFailed with the engine's own error text.
func (r *runner) call(ctx context.Context, command any) (json.RawMessage, error) {
	id := commandID(command)
	if err := r.session.writeMessage(command); err != nil {
		return nil, err
	}
	for {
		in, err := r.session.next(ctx, r.limits.IdleTimeout)
		if err != nil {
			return nil, r.session.wrapWait(err)
		}
		if in.err != nil {
			return nil, r.session.classifyStreamError(in.err)
		}
		f := in.frame
		if f.Type == frameResponse && f.ID == id {
			if f.Success == nil || !*f.Success {
				return nil, fmt.Errorf("%w: %s: %s", ErrCommandFailed, f.Command, f.Error)
			}
			return f.Data, nil
		}
		if err := r.handle(ctx, in); err != nil {
			return nil, err
		}
	}
}

// commandID reads the id off one of Babel's command values.
func commandID(command any) string {
	switch c := command.(type) {
	case negotiateCommand:
		return c.ID
	case setHostToolsCommand:
		return c.ID
	case promptCommand:
		return c.ID
	case plainCommand:
		return c.ID
	}
	return ""
}

// errDrained is the internal signal that Babel stopped reading stdout because
// the child exited and something else is holding the pipe open. It is not a
// protocol failure by itself.
var errDrained = errors.New("worker: stopped reading after exit")

// loop supervises the stream until the model's turn ends, and returns the
// first fatal protocol or supervision failure. A nil return means the turn
// ended, which does not yet mean the run produced a result.
func (r *runner) loop(ctx context.Context) error {
	s := r.session
	idle := time.NewTimer(r.limits.IdleTimeout)
	defer idle.Stop()
	var drain *time.Timer
	defer func() {
		if drain != nil {
			drain.Stop()
		}
	}()

	inbox := s.inbound
	reaped := s.reaped
	for !r.ended {
		select {
		case in := <-inbox:
			if in.err != nil {
				if errors.Is(in.err, io.EOF) {
					// The engine closed stdout before its turn ended: it
					// died, or Code did. The exit status says which.
					return s.classifyStreamError(in.err)
				}
				return in.err
			}
			stopTimer(idle)
			idle.Reset(r.limits.IdleTimeout)
			if err := r.handle(ctx, in); err != nil {
				return err
			}

		case <-reaped:
			reaped = nil
			if drain == nil {
				drain = time.NewTimer(r.limits.DrainGrace)
			}

		case <-ctx.Done():
			// The engine's own abort is the courteous half; teardown's
			// kill is the safety net.
			_ = s.writeMessage(plainCommand{ID: r.ids.next(), Type: commandAbort})
			return ctx.Err()

		case <-idle.C:
			return fmt.Errorf("%w: no frame for %s", ErrWorkerStalled, r.limits.IdleTimeout)

		case <-timerChan(drain):
			return errDrained
		}
	}
	return nil
}

// handle applies one inbound frame that is not the response Babel is waiting
// for.
func (r *runner) handle(ctx context.Context, in inbound) error {
	f := in.frame
	r.events++
	if r.events > r.limits.MaxEvents {
		return fmt.Errorf("%w: more than %d frames", ErrEventBudget, r.limits.MaxEvents)
	}
	at := time.Now().UTC()

	switch f.Type {
	case frameHostToolCall:
		return r.handleToolCall(ctx, f, at)
	case frameHostToolCancel:
		// Every call is answered before the next frame is read, so a
		// cancellation can only name a call already answered. It is noted
		// and nothing is withdrawn.
		r.recordProgress("tool", "the engine withdrew call "+f.TargetID+" after it was answered", at)
		return nil
	case frameAgentEnd:
		if f.IsTerminal == nil || *f.IsTerminal {
			r.ended = true
		}
		r.recordProgress("agent", "turn ended"+lastStop(f.Messages), at)
		return nil
	case framePromptResult:
		if f.AgentInvoked != nil && !*f.AgentInvoked {
			r.ended = true
		}
		return nil
	case frameResponse:
		// A response to nothing Babel is waiting for: a late error for an
		// accepted prompt, or a stray. The prompt's async failure is the
		// one that matters.
		if f.Command == commandPrompt && f.Success != nil && !*f.Success {
			return fmt.Errorf("%w: prompt: %s", ErrCommandFailed, f.Error)
		}
		return nil
	case frameExtensionUI:
		return r.session.writeMessage(extensionUIResponse{Type: "extension_ui_response", ID: f.ID, Cancelled: true})
	case frameHostURIRequest:
		return r.session.writeMessage(hostURIResult{Type: "host_uri_result", ID: f.ID, IsError: true,
			Error: "Babel registers no URI schemes"})
	case frameMessageEnd:
		var message struct {
			Role string `json:"role"`
			AssistantMessageAccounting
		}
		if len(f.Message) != 0 {
			if err := json.Unmarshal(f.Message, &message); err != nil {
				return fmt.Errorf("%w: message_end message: %v", ErrMalformedFrame, err)
			}
		}
		if message.Role == "assistant" {
			record := message.AssistantMessageAccounting
			record.Seq, record.At = r.events, at
			r.receipt.AssistantMessages = append(r.receipt.AssistantMessages, record)
		}
		return nil
	case frameRetryFallback, frameFallbackSucceeded:
		record := FallbackRecord{Seq: r.events, At: at, Type: f.Type, Role: f.Role}
		if f.Type == frameRetryFallback {
			record.From, record.To = f.From, f.To
		} else if len(f.Model) != 0 {
			if err := json.Unmarshal(f.Model, &record.Model); err != nil {
				return fmt.Errorf("%w: fallback model: %v", ErrMalformedFrame, err)
			}
		}
		r.receipt.Fallbacks = append(r.receipt.Fallbacks, record)
		r.recordProgress("agent", f.Type, at)
		return nil
	case frameAgentStart, frameTurnStart, frameTurnEnd, frameToolStart, frameToolEnd,
		frameCompactStart, frameCompactEnd, frameRetryStart, frameRetryEnd:
		r.recordProgress("agent", f.Type, at)
		return nil
	case frameModelChanged:
		r.recordProgress("model", "model changed: "+string(bytes.TrimSpace(f.Model)), at)
		return nil
	case frameExtensionErr:
		r.recordProgress("extension", "extension error in "+f.Event, at)
		return nil
	case frameReady:
		return fmt.Errorf("%w: a second ready frame", ErrProtocolMismatch)
	}
	if f.Type == "" {
		return fmt.Errorf("%w: frame without a type", ErrMalformedFrame)
	}
	r.unknown[f.Type] = struct{}{}
	return nil
}

// recordProgress keeps a bounded lifecycle trail and notifies the caller so
// an interface can stay responsive while the run is in flight (SPEC.md §2.6).
func (r *runner) recordProgress(stage, message string, at time.Time) {
	r.progress++
	record := ProgressRecord{Seq: r.progress, Stage: stage, Message: message, At: at}
	if len(r.receipt.Progress) < r.limits.MaxProgressRecords {
		r.receipt.Progress = append(r.receipt.Progress, record)
	} else {
		r.receipt.ProgressDropped++
	}
	if r.client.cfg.OnProgress != nil {
		r.client.cfg.OnProgress(record)
	}
}

// handleToolCall answers one host_tool_call: a submission is validated and
// recorded, an evidence call is authorized and served, and anything else is
// refused. A refusal is answered, not fatal: the run continues (SPEC.md §2.6).
func (r *runner) handleToolCall(ctx context.Context, f frame, at time.Time) error {
	if f.ID == "" {
		return fmt.Errorf("%w: host_tool_call without an id, which cannot be answered", ErrMalformedFrame)
	}
	r.toolCount++
	if r.toolCount > r.limits.MaxToolRequests+toolBudgetSlack {
		return fmt.Errorf("%w: %d calls against a budget of %d", ErrToolBudget, r.toolCount, r.limits.MaxToolRequests)
	}
	started := time.Now()
	record := ToolRecord{
		Index:           r.toolCount,
		RequestID:       f.ID,
		ToolCallID:      f.ToolCallID,
		Tool:            f.ToolName,
		ArgumentsDigest: argumentsDigest(f.Arguments),
		ArgumentsBytes:  len(f.Arguments),
		At:              at,
	}

	var answer hostToolResult
	switch {
	case f.ToolName == ToolSubmit:
		reason, accepted := r.submit(f.Arguments, at)
		record.Allowed = accepted
		record.Reason = reason
		if !accepted {
			record.DenyCode = DenyPolicy
		}
		answer = textResult(f.ID, reason, !accepted)
	default:
		code, decision := r.decide(ctx, f)
		record.Capability = r.capabilityOf(f.ToolName)
		record.Allowed = decision.Allow
		record.DenyCode = code
		record.Reason = decision.Reason
		if decision.Allow {
			text := string(decision.Results)
			if len(decision.Results) == 0 {
				text = decision.Reason
			}
			answer = textResult(f.ID, text, false)
		} else {
			answer = textResult(f.ID, "refused ("+string(code)+"): "+decision.Reason, true)
		}
	}
	record.Decided = time.Since(started)
	// The receipt records the decision and the reason the model was given,
	// and never the served payload. That split is §9: the pipe carries
	// content to the model because a model that cannot read a record cannot
	// form an observation about it, while the durable record an operator
	// exports keeps locators and digests only.
	r.receipt.ToolRequests = append(r.receipt.ToolRequests, record)
	return r.session.writeMessage(answer)
}

// capabilityOf resolves a registered tool name to its capability.
func (r *runner) capabilityOf(name string) Capability {
	for _, tool := range r.job.Tools {
		if tool.Name == name {
			return tool.Capability
		}
	}
	return ""
}

// decide applies the fixed authorization order for an evidence call. The
// registration is checked before the policy, so a permissive policy can never
// widen a run's boundary.
//
// A denial returns no payload even when the policy attached one. Served
// evidence is what an allowed call produced; a facility that both refused a
// call and answered it would be sending two contradictory things down one
// pipe.
func (r *runner) decide(ctx context.Context, f frame) (DenyCode, Decision) {
	capability := r.capabilityOf(f.ToolName)
	if capability == "" {
		return DenyUnknownTool, Decision{Reason: "the job registered no tool named " + f.ToolName}
	}
	if !r.job.Grant.ExpiresAt.IsZero() && time.Now().After(r.job.Grant.ExpiresAt) {
		return DenyPolicy, Decision{Reason: "the run's capability grant has expired"}
	}
	if r.toolCount > r.limits.MaxToolRequests {
		return DenyLimit, Decision{Reason: "tool call budget exhausted"}
	}
	decision := r.client.authorizer().Authorize(ctx, ToolRequest{
		JobID:      r.job.JobID,
		RunID:      r.job.RunID,
		Index:      r.toolCount,
		RequestID:  f.ID,
		ToolCallID: f.ToolCallID,
		Capability: capability,
		Tool:       f.ToolName,
		Arguments:  f.Arguments,
		Grant:      r.job.Grant,
	})
	if !decision.Allow {
		return DenyPolicy, Decision{Reason: decision.Reason}
	}
	return "", decision
}

// submit records one call to the submit tool. The engine validated the
// arguments against the job's schema; the job's own Accept decides the rest,
// and a refusal is what the model reads back so it can correct itself. An
// accepted submission replaces the earlier one; a refused one never does.
func (r *runner) submit(arguments json.RawMessage, at time.Time) (string, bool) {
	r.receipt.Submissions++
	if len(arguments) == 0 || !json.Valid(arguments) {
		return "the submission carries no JSON arguments", false
	}
	if r.job.Accept != nil {
		if err := r.job.Accept(arguments); err != nil {
			return "submission refused: " + err.Error(), false
		}
	}
	payload := make(json.RawMessage, len(arguments))
	copy(payload, arguments)
	r.receipt.Result = &ResultRecord{
		Status:  StatusOK,
		Schema:  r.job.Output.Schema,
		Payload: payload,
		At:      at,
	}
	return "submission accepted as the job's result", true
}

// stats asks the engine for its own accounting once the turn has ended. A
// refusal or a malformed answer is recorded as no usage rather than as a run
// failure: the result is already in hand.
func (r *runner) stats(ctx context.Context) {
	data, err := r.call(ctx, plainCommand{ID: r.ids.next(), Type: commandSessionStats})
	if err != nil {
		return
	}
	if usage, err := usageOf(data); err == nil {
		r.receipt.Usage = usage
	}
}

// finish ends the process: stdin closes, the tree is given the exit grace,
// and what is still alive after that is killed. The final verdict follows the
// precedence supervision failure > missing result > dirty exit.
func (r *runner) finish(ctx context.Context, fatal error) error {
	s := r.session
	if fatal != nil {
		s.teardown(true)
		if errors.Is(fatal, errDrained) {
			fatal = nil
		}
	} else {
		_ = s.stdinW.Close()
		lingered := s.awaitExit(r.limits.ExitGrace)
		s.teardown(lingered != nil)
		if lingered != nil {
			fatal = lingered
		}
	}
	if fatal != nil {
		return fatal
	}
	if r.receipt.Result == nil {
		if !r.invoked {
			return fmt.Errorf("%w: the engine completed the prompt without a model turn", ErrNoResult)
		}
		return fmt.Errorf("%w: %d submission(s), none accepted", ErrNoResult, r.receipt.Submissions)
	}
	if code := s.exitCode(); code != 0 {
		return fmt.Errorf("%w: exit status %d", ErrDirtyExit, code)
	}
	return nil
}

// readFinishedReport reads Code's post-exit report for the measurements it
// carries. It is best effort by contract: a wrapper killed before it could
// write the report leaves the launch report in place, and Babel claims no
// measurement from it.
func (r *runner) readFinishedReport() {
	info, err := readRuntimeInfo(r.runtimePath)
	if err != nil || !info.Finished {
		return
	}
	r.finished = info
	r.receipt.Resources = info.Resources
	if info.ExitCode != nil {
		r.receipt.ExitCode = *info.ExitCode
	}
}

// argumentsDigest fingerprints tool arguments without retaining them. A
// digest of an empty document is the empty digest, so a call with no
// arguments produces no record at all.
func argumentsDigest(arguments json.RawMessage) digest.Digest {
	if len(arguments) == 0 {
		return ""
	}
	if len(arguments) > maxArgumentDigestBytes {
		arguments = arguments[:maxArgumentDigestBytes]
	}
	return digest.Bytes(arguments)
}

// failureCode names the Babel-side failure a receipt records.
func failureCode(err error) string {
	for _, candidate := range []struct {
		sentinel error
		code     string
	}{
		{ErrHandshakeTimeout, "handshake-timeout"},
		{ErrProtocolMismatch, "protocol-mismatch"},
		{ErrRuntimeInfo, "runtime-info"},
		{ErrProfileMismatch, "profile-mismatch"},
		{ErrPlatformUnqualified, "platform-unqualified"},
		{ErrContainment, "containment"},
		{ErrSecretDeclared, "secret-declared"},
		{ErrOversizedFrame, "oversized-frame"},
		{ErrMalformedFrame, "malformed-frame"},
		{ErrCommandFailed, "command-failed"},
		{ErrNoResult, "no-result"},
		{ErrEngineExited, "engine-exited"},
		{ErrWorkerStalled, "stalled"},
		{ErrWorkerLingered, "lingered"},
		{ErrDirtyExit, "dirty-exit"},
		{ErrEventBudget, "event-budget"},
		{ErrToolBudget, "tool-budget"},
	} {
		if errors.Is(err, candidate.sentinel) {
			return candidate.code
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline-exceeded"
	}
	return "supervision-failure"
}

// failureOrigin attributes a failure: the engine's own exit, silence or
// refusal to leave is the far side's; everything else is Babel's supervision
// or a boundary the far side broke.
func failureOrigin(err error) string {
	for _, sentinel := range []error{ErrDirtyExit, ErrEngineExited, ErrWorkerStalled, ErrWorkerLingered} {
		if errors.Is(err, sentinel) {
			return FailureWorker
		}
	}
	return FailureBabel
}

// lastStop reads the last assistant message's stop reason and error text out
// of an agent_end, so a turn the provider ended — an authentication failure,
// retries exhausted — is legible in the receipt beside one the model ended.
func lastStop(messages json.RawMessage) string {
	var list []struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(messages, &list) != nil {
		return ""
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Role != "assistant" {
			continue
		}
		if list[i].StopReason == "" {
			return ""
		}
		out := " (" + list[i].StopReason
		if list[i].ErrorMessage != "" {
			out += ": " + list[i].ErrorMessage
		}
		return out + ")"
	}
	return ""
}

// sortedKeys renders a set deterministically for a receipt.
func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// timerChan yields a timer's channel, or nil for an unarmed timer so the
// select case is disabled.
func timerChan(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// stopTimer stops a timer and drains a value it may already have delivered,
// which is what makes a subsequent Reset well-defined.
func stopTimer(t *time.Timer) {
	if t.Stop() {
		return
	}
	select {
	case <-t.C:
	default:
	}
}

// inbound is one frame read from the engine, or the reason reading stopped.
type inbound struct {
	frame frame
	err   error
}

// session is one launched process and the goroutines reading it.
type session struct {
	cmd     *exec.Cmd
	pgid    int
	limits  Limits
	stdinW  *os.File
	stdoutR *os.File
	stderrR *os.File

	inbound chan inbound
	stop    chan struct{}
	reaped  chan struct{}
	tail    *tail
	wg      sync.WaitGroup

	killOnce sync.Once
	downOnce sync.Once
}

// start launches the engine and its supervision goroutines.
func (c *Client) start(ctx context.Context, limits Limits, args []string) (*session, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("worker start: %w", err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("worker start: stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW)
		return nil, fmt.Errorf("worker start: stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, fmt.Errorf("worker start: stderr pipe: %w", err)
	}

	cmd := exec.Command(c.cfg.Binary, args...)
	cmd.Dir = c.cfg.Dir
	cmd.Env = c.env()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	// The child leads its own process group, so cancellation reaches every
	// process it spawns and not just the one Babel launched.
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		closeAll(inR, inW, outR, outW, errR, errW)
		return nil, fmt.Errorf("worker start: %w", err)
	}
	// The parent's copies of the child's ends must go, or stdout never reaches
	// EOF and the child never sees stdin close.
	closeAll(inR, outW, errW)

	s := &session{
		cmd:     cmd,
		pgid:    cmd.Process.Pid,
		limits:  limits,
		stdinW:  inW,
		stdoutR: outR,
		stderrR: errR,
		inbound: make(chan inbound),
		stop:    make(chan struct{}),
		reaped:  make(chan struct{}),
		tail:    &tail{limit: limits.StderrTailBytes},
	}

	s.wg.Add(3)
	go s.readFrames()
	go s.readDiagnostics(c.cfg.Diagnostics)
	go func() {
		defer s.wg.Done()
		// The exit status is read from cmd.ProcessState after reaped closes;
		// Wait's own error adds nothing a caller can act on that the status
		// and the stderr tail do not already carry.
		_ = cmd.Wait()
		close(s.reaped)
	}()
	return s, nil
}

// readFrames parses the engine's stdout into inbound values. Every stop is
// reported exactly once: EOF, an oversized or malformed frame, or a read
// failure.
func (s *session) readFrames() {
	defer s.wg.Done()
	reader := newFrameReader(s.stdoutR, s.limits.MaxFrameBytes, s.limits.MaxReassembledBytes)
	for {
		f, _, err := reader.next()
		if err == nil {
			if !s.deliver(inbound{frame: f}) {
				return
			}
			continue
		}
		switch {
		case errors.Is(err, io.EOF):
			s.deliver(inbound{err: io.EOF})
		case errors.Is(err, os.ErrClosed):
			// Teardown closed the pipe; nobody is listening any more.
		default:
			s.deliver(inbound{err: err})
		}
		return
	}
}

// deliver hands one inbound to the supervisor, or gives up when supervision
// has ended. It is what keeps this goroutine from outliving Run.
func (s *session) deliver(in inbound) bool {
	select {
	case s.inbound <- in:
		return true
	case <-s.stop:
		return false
	}
}

// readDiagnostics drains the engine's stderr into the bounded tail and the
// optional diagnostics sink. It is never parsed: stderr carries Code's and the
// engine's own logging, and treating it as protocol would let a log line
// steer a run.
//
// Each line is bounded before it is retained. A process that writes a
// gigabyte without a newline is misbehaving, and reading that into memory to
// log it would let it take Babel down.
func (s *session) readDiagnostics(sink io.Writer) {
	defer s.wg.Done()
	reader := bufio.NewReaderSize(s.stderrR, readBufferSize)
	for {
		line, truncated, err := readDiagnosticLine(reader, stderrLineLimit)
		if trimmed := strings.TrimRight(string(line), "\r\n"); trimmed != "" {
			if truncated {
				trimmed += " [truncated]"
			}
			s.tail.writeLine(trimmed)
			if sink != nil {
				fmt.Fprintf(sink, "engine: %s\n", trimmed)
			}
		}
		if err != nil {
			return
		}
	}
}

// readDiagnosticLine reads one stderr line, keeping at most max bytes and
// discarding the rest of an over-long line rather than buffering it.
func readDiagnosticLine(reader *bufio.Reader, max int) (line []byte, truncated bool, err error) {
	for {
		chunk, readErr := reader.ReadSlice('\n')
		switch room := max - len(line); {
		case room >= len(chunk):
			line = append(line, chunk...)
		case room > 0:
			line = append(line, chunk[:room]...)
			truncated = true
		case len(chunk) > 0:
			truncated = true
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return line, truncated, readErr
	}
}

// writeMessage encodes one Babel-to-engine message as a single line.
func (s *session) writeMessage(msg any) error {
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("worker: encoding message: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := s.stdinW.Write(encoded); err != nil {
		return fmt.Errorf("worker: writing to engine stdin: %w", err)
	}
	return nil
}

// next waits for the next inbound value, for at most budget. A child that
// exits mid-wait does not end the wait immediately: frames it already wrote
// may still be in the pipe, so the budget is replaced by the shorter drain
// grace.
func (s *session) next(ctx context.Context, budget time.Duration) (inbound, error) {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	reaped := s.reaped
	for {
		select {
		case in := <-s.inbound:
			return in, nil
		case <-reaped:
			reaped = nil
			timer.Stop()
			timer = time.NewTimer(s.limits.DrainGrace)
		case <-ctx.Done():
			return inbound{}, ctx.Err()
		case <-timer.C:
			return inbound{}, errTimeout
		}
	}
}

// errTimeout is the internal "budget elapsed" signal; callers translate it
// into the sentinel that fits what they were waiting for.
var errTimeout = errors.New("worker: wait budget elapsed")

// refuse ends a launch Babel will not prompt: stdin closes with nothing
// written, the tree gets the grace to leave on its own, and what remains is
// killed. The wait is part of the contract rather than politeness: a refused
// engine must exit on EOF, and killing it the instant stdin closes would make
// that obligation unobservable.
func (s *session) refuse(grace time.Duration) error {
	_ = s.stdinW.Close()
	err := s.awaitExit(grace)
	s.teardown(err != nil)
	return err
}

// classifyStreamError turns a reader failure during a synchronous wait into
// the sentinel that describes it. EOF before the turn has ended is the engine
// leaving, whatever else was pending; the exit status says how.
func (s *session) classifyStreamError(err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: engine closed its stdout, exit status %d", ErrEngineExited, s.exitCode())
	}
	return err
}

// wrapWait translates a wait failure: an elapsed budget means the engine went
// quiet, unless it has already exited, in which case it simply never
// answered.
func (s *session) wrapWait(err error) error {
	if !errors.Is(err, errTimeout) {
		return err
	}
	if s.hasExited() {
		return fmt.Errorf("%w: engine exited %d without answering", ErrEngineExited, s.exitCode())
	}
	return fmt.Errorf("%w: no frame for %s", ErrWorkerStalled, s.limits.IdleTimeout)
}

// awaitExit waits for the process to exit within grace.
func (s *session) awaitExit(grace time.Duration) error {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.reaped:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w: still running %s after its stdin closed", ErrWorkerLingered, grace)
	}
}

// hasExited reports whether the direct child has been reaped.
func (s *session) hasExited() bool {
	select {
	case <-s.reaped:
		return true
	default:
		return false
	}
}

// exitCode is the child's exit status, or -1 when it was signalled or has not
// exited.
func (s *session) exitCode() int {
	if !s.hasExited() || s.cmd.ProcessState == nil {
		return -1
	}
	return s.cmd.ProcessState.ExitCode()
}

// kill terminates the whole process group: SIGTERM, then SIGKILL after the
// terminate grace. Signalling the group rather than the pid is what makes the
// guarantee whole — the sandbox Code spawned is in that group, and killing
// only the direct child would leave it running.
//
// The group is signalled before the child is reaped wherever Babel initiates
// the shutdown, so the process-group ID cannot have been recycled: a group ID
// stays reserved while any member lives.
func (s *session) kill() {
	s.killOnce.Do(func() {
		_ = terminateTree(s.cmd, s.pgid, true)
		timer := time.NewTimer(s.limits.TerminateGrace)
		defer timer.Stop()
		select {
		case <-s.reaped:
		case <-timer.C:
		}
		_ = terminateTree(s.cmd, s.pgid, false)
	})
}

// teardown ends the session: the tree is killed when killTree is set, the
// direct child is reaped, the pipes are closed and every goroutine is joined.
// Nothing this session started outlives this call.
func (s *session) teardown(killTree bool) {
	s.downOnce.Do(func() {
		if killTree {
			s.kill()
		}
		<-s.reaped
		// Closing the read ends releases readers that are blocked on a pipe a
		// descendant still holds open. Babel owns both ends of these pipes, so
		// this is deterministic rather than racy.
		closeAll(s.stdinW, s.stdoutR, s.stderrR)
		close(s.stop)
		s.wg.Wait()
	})
}

// closeAll closes every non-nil file, ignoring errors: these are pipe ends
// being released during teardown, where there is nothing left to do about a
// failure.
func closeAll(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

// containsString reports whether values holds want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// tail keeps at most the last limit bytes of an engine-written stream, so a
// runaway child cannot balloon a receipt or an error message. The bound is
// the whole point: it is the one idiom this package uses for retaining
// anything the far side controls.
type tail struct {
	mu      sync.Mutex
	limit   int
	buf     []byte
	dropped bool
}

// Write retains a stream's lines, so a tail can stand in for a process's
// stderr directly.
func (t *tail) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			t.writeLine(line)
		}
	}
	return len(p), nil
}

// writeLine appends one line.
func (t *tail) writeLine(line string) {
	if t.limit <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > 0 {
		t.buf = append(t.buf, '\n')
	}
	t.buf = append(t.buf, line...)
	if excess := len(t.buf) - t.limit; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
		t.dropped = true
	}
}

// String renders the retained tail as one line so it composes with wrapped
// errors. Truncation is marked with a leading ellipsis.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) == 0 {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(string(t.buf), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	joined := strings.Join(parts, "; ")
	if t.dropped && joined != "" {
		return "..." + joined
	}
	return joined
}
