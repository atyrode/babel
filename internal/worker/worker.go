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
	defaultHandshakeTimeout = 10 * time.Second
	defaultIdleTimeout      = 2 * time.Minute
	defaultExitGrace        = 5 * time.Second
	defaultTerminateGrace   = 2 * time.Second
	defaultDrainGrace       = 2 * time.Second
	defaultMaxLineBytes     = 1 << 20
	defaultMaxEvents        = 100_000
	defaultMaxToolRequests  = 1024
	defaultMaxProgress      = 256
	defaultStderrTailBytes  = 4 << 10

	// readBufferSize is the stdout read buffer. Lines are usually short; the
	// oversize check is enforced against Limits.MaxLineBytes independently of
	// this, so the buffer is a throughput choice and not a protocol one.
	readBufferSize = 64 << 10

	// stderrLineLimit bounds one retained diagnostic line. Stderr is not
	// protocol, so an over-long line is truncated rather than fatal — but it
	// is bounded, because buffering a worker's runaway log line would let it
	// exhaust Babel's memory.
	stderrLineLimit = 8 << 10

	// toolBudgetSlack is how many over-budget tool requests a worker may make
	// before Babel gives up on it. Every one of them is denied with
	// DenyLimit, so a worker that keeps asking is looping rather than
	// adapting, and a run that cannot progress must end rather than spin.
	toolBudgetSlack = 16

	// maxArgumentDigestBytes bounds how much of a tool argument blob is read
	// to digest it. Arguments are never stored, only fingerprinted.
	maxArgumentDigestBytes = 1 << 20

	// redactedMarker replaces a job secret wherever worker-controlled text is
	// recorded or reported.
	redactedMarker = "[redacted]"

	// minSecretLength is the shortest value worth scrubbing. A short secret
	// would match innocuous substrings everywhere and turn every diagnostic
	// into noise; Babel-issued run tokens are long by construction.
	minSecretLength = 8

	// rawTranscriptBytes bounds the conformance suite's raw transcript. It is
	// larger than the stderr tail because an event line may legitimately be
	// long, and small enough that a worker writing without end cannot make
	// the grader itself the failure.
	rawTranscriptBytes = 64 << 10
)

// Decision is one authorization outcome from the injected policy. Reason is
// recorded in the receipt and sent to the worker, so it must explain the
// decision without disclosing anything the worker is not cleared to see.
type Decision struct {
	Allow  bool
	Reason string
}

// ToolRequest is one worker request for an evidence or execution capability,
// as handed to the policy. Arguments are the worker's raw JSON: the policy
// sees them, the receipt never does.
type ToolRequest struct {
	JobID      string
	RunID      string
	Index      int
	RequestID  string
	Capability Capability
	Tool       string
	Arguments  json.RawMessage
	Reason     string
	Grant      Grant
}

// Authorizer decides tool requests. Babel authorizes every one of them
// (SPEC.md §6.5), and the grant is checked before the policy runs, so an
// Authorizer can only narrow what a run may do.
type Authorizer interface {
	Authorize(ctx context.Context, req ToolRequest) Decision
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, req ToolRequest) Decision

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, req ToolRequest) Decision {
	return f(ctx, req)
}

// AllowWithinGrant is the permissive policy: it allows anything the run's
// capability grant already covers. It is not "allow everything" — the grant
// check runs first and is not bypassable — but it delegates the whole
// decision to the grant, so it belongs in development and tests rather than
// in a run whose scope was negotiated with an operator.
func AllowWithinGrant() Authorizer {
	return AuthorizerFunc(func(context.Context, ToolRequest) Decision {
		return Decision{Allow: true, Reason: "within grant"}
	})
}

// DenyAll refuses every request with the given reason. It is the default when
// no Authorizer is configured: a run with no policy is not a run with a
// permissive policy.
func DenyAll(reason string) Authorizer {
	return AuthorizerFunc(func(context.Context, ToolRequest) Decision {
		return Decision{Allow: false, Reason: reason}
	})
}

// Limits bounds the transport and the shutdown, not the analysis. Zero fields
// select the documented default.
type Limits struct {
	// HandshakeTimeout bounds the wait for the worker's hello.
	HandshakeTimeout time.Duration

	// IdleTimeout bounds the gap between events. Stderr output does not reset
	// it: a worker that talks only on stderr is stalled, and the whole point
	// of the timer is to notice that.
	IdleTimeout time.Duration

	// ExitGrace bounds how long a worker may take to exit after its terminal
	// event before Babel kills the tree (ErrWorkerLingered).
	ExitGrace time.Duration

	// TerminateGrace is how long SIGTERM is given before SIGKILL when Babel
	// tears the tree down.
	TerminateGrace time.Duration

	// DrainGrace bounds how long Babel keeps reading stdout after the child
	// has exited. A grandchild holding the pipe open would otherwise keep the
	// stream from ever reaching EOF.
	DrainGrace time.Duration

	// MaxLineBytes is the largest event line accepted (ErrOversizedLine).
	MaxLineBytes int

	// MaxEvents bounds the whole stream (ErrEventBudget).
	MaxEvents int

	// MaxToolRequests bounds authorized requests; further ones are denied
	// with DenyLimit.
	MaxToolRequests int

	// MaxProgressRecords bounds how many progress events a receipt keeps. A
	// chatty worker must not make the audit record unbounded, so the excess
	// is counted instead of stored.
	MaxProgressRecords int

	// StderrTailBytes bounds the retained tail of worker diagnostics.
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
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = defaultMaxLineBytes
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

// onWire renders the limits the worker must respect.
func (l Limits) onWire() limitsOnWire {
	return limitsOnWire{
		MaxLineBytes:    l.MaxLineBytes,
		MaxEvents:       l.MaxEvents,
		MaxToolRequests: l.MaxToolRequests,
		IdleSeconds:     l.IdleTimeout.Seconds(),
		ExitGraceSecs:   l.ExitGrace.Seconds(),
	}
}

// Config describes how to launch and supervise one worker process.
type Config struct {
	// Binary is the worker executable. Required.
	Binary string

	// Args are extra arguments. They must carry no secrets: argv is visible
	// in any process listing. The mode and the job travel on stdin.
	Args []string

	// Dir is the child's working directory. Empty means the parent's.
	Dir string

	// Env is appended to a minimal derived environment (HOME, PATH, TMPDIR,
	// LANG when the parent has them). It must carry no credentials, for the
	// same reason Args must not.
	Env []string

	// Versions is the protocol version set Babel offers. Nil means
	// DefaultVersions.
	Versions []int

	// Authorizer decides tool requests. Nil fails closed: every request is
	// denied.
	Authorizer Authorizer

	// Limits bounds the transport and the shutdown.
	Limits Limits

	// Diagnostics receives the worker's stderr, one line at a time, with job
	// secrets scrubbed. Nil discards it. The bounded tail is recorded in the
	// receipt regardless.
	Diagnostics io.Writer

	// Requirement is the containment the worker must declare. The zero value
	// means SandboxedRun: the strict setting is the default deliberately,
	// because a permissive default would quietly become the norm and the
	// operator who wants to relax it should have to say so per run. Set
	// Unsandboxed for a run that genuinely needs no boundary, such as a
	// configuration-only probe against a local worker.
	Requirement *Requirement

	// OnProgress is called for each progress event as it arrives, so a
	// caller's interface stays responsive while a run is in flight (SPEC.md
	// §2.6). It runs on the supervision goroutine and must not block: a slow
	// callback delays the next tool authorization.
	OnProgress func(ProgressRecord)

	// rawTranscript captures the worker's stdout and stderr exactly as the
	// worker wrote them — unscrubbed, credential included — and exists for
	// one caller: the run/no-credential-leak obligation, which grades whether
	// the worker itself keeps the broker token out of its own output. That
	// cannot be graded from anything Babel stores, because Babel scrubs the
	// token on the way in and a scrubbed record looks identical whether the
	// worker was disciplined or not.
	//
	// It is unexported for exactly that reason. No caller outside this
	// package can ask for unscrubbed worker output, the zero value is no
	// tee, and nothing in production sets it. The capture is bounded
	// (rawTranscriptBytes), lives only in memory for the length of one
	// obligation, and is never written to a file, a log or a diagnostic
	// sink. It observes; it does not change what Babel parses or stores.
	rawTranscript *tail
}

// Client supervises worker processes. One Client may run many jobs; each Run
// or Configure launches, supervises and reaps its own process.
type Client struct {
	cfg Config
}

// New validates cfg and returns a Client. It performs no I/O: the binary is
// resolved when a process is launched, so New never blocks and never reports
// whether a worker is installed.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("worker: binary is required")
	}
	return &Client{cfg: cfg}, nil
}

// versions is the offered version set.
func (c *Client) versions() []int {
	if len(c.cfg.Versions) == 0 {
		return DefaultVersions()
	}
	return c.cfg.Versions
}

// authorizer is the configured policy, failing closed when absent.
func (c *Client) authorizer() Authorizer {
	if c.cfg.Authorizer == nil {
		return DenyAll("no authorizer configured")
	}
	return c.cfg.Authorizer
}

// env builds the child environment: the few variables a subprocess
// legitimately needs plus whatever the caller adds. Nothing else is
// inherited, and no secret is ever placed here — the evidence-broker token
// travels on stdin, where a process listing cannot see it.
func (c *Client) env() []string {
	env := make([]string, 0, 4+len(c.cfg.Env))
	for _, key := range [...]string{"HOME", "PATH", "TMPDIR", "LANG"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, c.cfg.Env...)
}

// Configure runs the worker in configuration-only mode: it opens Code's own
// dials, saves the profile under Code's ownership, reports the reference plus
// non-secret privacy/cost/capability metadata, and exits without launching
// OMP (SPEC.md §2.6).
//
// Babel persists only what this returns. A worker that declares
// credential-shaped metadata fails with ErrSecretDeclared rather than having
// the offending field quietly dropped.
func (c *Client) Configure(ctx context.Context) (*Configuration, error) {
	limits := c.cfg.Limits.withDefaults()
	s, err := c.start(ctx, limits, scrubber{})
	if err != nil {
		return nil, err
	}
	// A worker that already exited has nothing left to kill; one that has not
	// is torn down by process group, whichever way this returns.
	defer func() { s.teardown(!s.hasExited()) }()

	hello, version, err := s.handshake(ctx, ModeConfigure, c.versions())
	if err != nil {
		return nil, err
	}

	in, err := s.next(ctx, limits.IdleTimeout)
	if err != nil {
		return nil, s.wrapWait(err)
	}
	if in.err != nil {
		return nil, s.classifyStreamError(in.err)
	}
	if in.ev.Type != MessageConfiguration {
		return nil, fmt.Errorf("%w: configure mode answered with %q", ErrEventOrder, in.ev.Type)
	}
	if err := validateMetadata(in.ev.Metadata); err != nil {
		return nil, err
	}

	cfg := &Configuration{
		Profile:         in.ev.Profile,
		Privacy:         in.ev.Privacy,
		Cost:            in.ev.Cost,
		Capabilities:    in.ev.Capabilities,
		Metadata:        in.ev.Metadata,
		Worker:          hello.Worker,
		ProtocolVersion: version,
		Unknown:         in.unknown,
	}
	if len(in.unknown) > 0 {
		cfg.Extra = make(map[string]json.RawMessage, len(in.unknown))
		for _, name := range in.unknown {
			cfg.Extra[name] = in.fields[name]
		}
	}

	// Configuration mode must exit on its own once it has answered; a
	// process that keeps running has launched something, which is exactly
	// what this mode promises not to do.
	if err := s.awaitExit(limits.ExitGrace); err != nil {
		return cfg, err
	}
	if code := s.exitCode(); code != 0 {
		return cfg, fmt.Errorf("%w: configure mode exited %d", ErrDirtyExit, code)
	}
	return cfg, nil
}

// Run executes one analysis job and returns its receipt.
//
// Babel owns the whole boundary here (SPEC.md §2.6): the version handshake,
// authorization of every tool request, cancellation, the lifetime of the
// entire process tree, validation of every event, and the final status.
// Analysis is never detached — Run returns only after the tree is reaped and
// every reader goroutine has finished.
//
// A receipt is returned whenever the process started, including on failure:
// the receipt is the audit record of what happened, and a failed run is
// exactly when it is needed. It never contains a credential.
func (c *Client) Run(ctx context.Context, job Job) (*Receipt, error) {
	limits := c.cfg.Limits.withDefaults()
	scrub := newScrubber(job.secrets())

	s, err := c.start(ctx, limits, scrub)
	if err != nil {
		return nil, err
	}

	r := &runner{
		client:      c,
		session:     s,
		job:         job,
		limits:      limits,
		requirement: c.requirement(),
		scrub:       scrub,
		seen:        make(map[string]struct{}),
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
	r.receipt.StderrTail = scrub.clean(s.tail.String())
	r.receipt.UnknownFields = sortedKeys(r.unknown)
	if err != nil && r.receipt.Failure == nil {
		r.receipt.Failure = &FailureRecord{
			Origin:  FailureBabel,
			Code:    failureCode(err),
			Message: err.Error(),
			At:      r.receipt.FinishedAt,
		}
	}
	return r.receipt, err
}

// execute performs the handshake, writes the job, and supervises the stream.
// Teardown always runs; whether it kills the tree depends on whether anything
// in it is still alive when the stream ends.
func (r *runner) execute(ctx context.Context) error {
	s := r.session

	hello, version, err := s.handshake(ctx, ModeWorker, r.client.versions())
	if err != nil {
		s.teardown(true)
		return err
	}
	r.receipt.Worker = hello.Worker
	r.receipt.ProtocolVersion = version

	// Job material is written only after the handshake succeeded, so a worker
	// Babel refuses never sees the broker credential, the source selectors,
	// or the grant.
	if err := s.writeMessage(r.job); err != nil {
		s.teardown(true)
		return err
	}

	fatal := r.loop(ctx)
	s.teardown(fatal != nil || !r.readerDone)
	return r.finalize(fatal)
}

// runner holds the per-run supervision state. It is single-goroutine: only
// loop mutates it.
type runner struct {
	client  *Client
	session *session
	job     Job
	limits  Limits
	scrub   scrubber
	receipt *Receipt

	// requirement is the containment the run demands of the worker. Babel
	// does not implement the sandbox (decision 53), so this is the boundary
	// it can still refuse to proceed without.
	requirement Requirement

	lastSeq    int
	events     int
	toolCount  int
	sawConfig  bool
	terminal   string
	workerErr  *WorkerError
	readerDone bool
	exited     bool
	seen       map[string]struct{}
	unknown    map[string]struct{}
}

// errDrained is the internal signal that Babel stopped reading stdout because
// the child exited and something else is holding the pipe open. It is not a
// protocol failure by itself.
var errDrained = errors.New("worker: stopped reading after exit")

// loop supervises the event stream until it ends, and returns the first fatal
// protocol or supervision failure. A nil return means the stream ended
// cleanly, which does not yet mean the run succeeded.
func (r *runner) loop(ctx context.Context) error {
	s := r.session

	idle := time.NewTimer(r.limits.IdleTimeout)
	defer idle.Stop()
	var grace, drain *time.Timer
	defer func() {
		if grace != nil {
			grace.Stop()
		}
		if drain != nil {
			drain.Stop()
		}
	}()

	inbox := s.inbound
	reaped := s.reaped

	for !(r.readerDone && r.exited) {
		select {
		case in := <-inbox:
			if in.err != nil {
				r.readerDone = true
				inbox = nil
				if errors.Is(in.err, io.EOF) {
					// The stream is over, so the idle timer is measuring
					// silence in something that no longer exists. Only the
					// reap remains, and it gets the exit grace: a worker that
					// closes stdout and then keeps running would otherwise
					// block this loop with nothing left to observe.
					stopTimer(idle)
					if grace == nil {
						grace = time.NewTimer(r.limits.ExitGrace)
					}
					continue
				}
				return in.err
			}
			stopTimer(idle)
			idle.Reset(r.limits.IdleTimeout)
			if err := r.handle(ctx, in); err != nil {
				return err
			}
			if r.terminal != "" && grace == nil {
				grace = time.NewTimer(r.limits.ExitGrace)
			}

		case <-reaped:
			r.exited = true
			reaped = nil
			if !r.readerDone && drain == nil {
				drain = time.NewTimer(r.limits.DrainGrace)
			}

		case <-ctx.Done():
			return ctx.Err()

		case <-idle.C:
			return fmt.Errorf("%w: no event for %s", ErrWorkerStalled, r.limits.IdleTimeout)

		case <-timerChan(grace):
			return fmt.Errorf("%w: still running %s after %s",
				ErrWorkerLingered, r.limits.ExitGrace, r.lingerCause())

		case <-timerChan(drain):
			// The child is gone but its stdout is still open, which means
			// something it spawned inherited the pipe. Stop reading; teardown
			// kills the group.
			return errDrained
		}
	}
	return nil
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

// lingerCause names what the worker should already have exited after, so the
// error says which obligation was missed.
func (r *runner) lingerCause() string {
	if r.terminal != "" {
		return "its " + r.terminal + " event"
	}
	return "closing its stdout"
}

// handle validates and applies one event.
func (r *runner) handle(ctx context.Context, in inbound) error {
	ev := in.ev
	for _, name := range in.unknown {
		r.unknown[name] = struct{}{}
	}

	if !knownEventType(ev.Type) {
		return fmt.Errorf("%w: %q", ErrUnknownEventType, r.scrub.clean(ev.Type))
	}
	if r.terminal != "" {
		switch {
		case ev.Type == MessageResult && r.terminal == MessageError:
			return ErrResultAfterError
		case ev.Type == MessageResult:
			return ErrDuplicateResult
		default:
			return fmt.Errorf("%w: %s after %s", ErrEventAfterResult, ev.Type, r.terminal)
		}
	}
	r.events++
	if r.events > r.limits.MaxEvents {
		return fmt.Errorf("%w: more than %d events", ErrEventBudget, r.limits.MaxEvents)
	}
	if ev.Seq <= r.lastSeq {
		return fmt.Errorf("%w: seq %d follows %d", ErrSequence, ev.Seq, r.lastSeq)
	}
	r.lastSeq = ev.Seq
	at := eventTime(ev)

	switch ev.Type {
	case MessageConfiguration:
		return r.handleConfiguration(ev)
	case MessageProgress:
		if !r.sawConfig {
			return fmt.Errorf("%w: progress before the resolved configuration", ErrEventOrder)
		}
		r.recordProgress(ev, at)
		return nil
	case MessageToolRequest:
		if !r.sawConfig {
			return fmt.Errorf("%w: tool-request before the resolved configuration", ErrEventOrder)
		}
		return r.handleToolRequest(ctx, ev, at)
	case MessageResult:
		return r.handleResult(ev, at)
	case MessageError:
		return r.handleWorkerError(ev, at)
	}
	// knownEventType already rejected everything else.
	return fmt.Errorf("%w: %q", ErrUnknownEventType, ev.Type)
}

// handleConfiguration records the profile the worker actually resolved. The
// receipt needs it (SPEC.md §6.5), so it is required, must come first, and
// must name the profile the job named.
func (r *runner) handleConfiguration(ev event) error {
	if r.sawConfig {
		return fmt.Errorf("%w: a second configuration event", ErrEventOrder)
	}
	if r.events != 1 {
		return fmt.Errorf("%w: configuration must be the first event, not event %d", ErrEventOrder, r.events)
	}
	if err := validateMetadata(ev.Metadata); err != nil {
		return err
	}
	if ev.Profile != r.job.Profile {
		return fmt.Errorf("%w: job named %s, worker resolved %s",
			ErrProfileMismatch, r.job.Profile, ev.Profile)
	}
	// The containment check happens here, at the worker's first event, because
	// this is the earliest moment Babel knows what boundary the worker claims
	// and the last moment before the worker begins executing anything. Babel
	// does not implement the sandbox (decision 53), so refusing an
	// insufficient declaration is the whole of its leverage.
	if err := ev.Containment.Satisfies(r.requirement); err != nil {
		return err
	}
	r.sawConfig = true
	r.receipt.Privacy = ev.Privacy
	r.receipt.Cost = ev.Cost
	r.receipt.ResolvedCapabilities = ev.Capabilities
	r.receipt.Containment = ev.Containment
	r.receipt.Metadata = r.scrub.cleanMap(ev.Metadata)
	return nil
}

// recordProgress keeps a bounded progress trail, the latest observed resource
// use, and notifies the caller so an interface can stay responsive while the
// run is in flight (SPEC.md §2.6).
func (r *runner) recordProgress(ev event, at time.Time) {
	if ev.Resources != nil {
		observed := *ev.Resources
		r.receipt.Resources = &observed
	}
	record := ProgressRecord{
		Seq:      ev.Seq,
		Stage:    r.scrub.clean(ev.Stage),
		Message:  r.scrub.clean(ev.Message),
		Fraction: ev.Fraction,
		At:       at,
	}
	if len(r.receipt.Progress) < r.limits.MaxProgressRecords {
		r.receipt.Progress = append(r.receipt.Progress, record)
	} else {
		r.receipt.ProgressDropped++
	}
	if r.client.cfg.OnProgress != nil {
		r.client.cfg.OnProgress(record)
	}
}

// handleToolRequest authorizes one request, answers it on the worker's stdin,
// and records the decision. A denial is answered, not fatal: the run
// continues (SPEC.md §2.6).
func (r *runner) handleToolRequest(ctx context.Context, ev event, at time.Time) error {
	if ev.RequestID == "" {
		return fmt.Errorf("%w: tool-request without a request_id, which cannot be answered", ErrMalformedEvent)
	}
	r.toolCount++
	if r.toolCount > r.limits.MaxToolRequests+toolBudgetSlack {
		return fmt.Errorf("%w: %d requests against a budget of %d",
			ErrToolBudget, r.toolCount, r.limits.MaxToolRequests)
	}

	started := time.Now()
	allow, code, reason := r.decide(ctx, ev)
	record := ToolRecord{
		Index:           r.toolCount,
		RequestID:       ev.RequestID,
		Capability:      ev.Capability,
		Tool:            r.scrub.clean(ev.Tool),
		ArgumentsDigest: argumentsDigest(ev.Arguments),
		ArgumentsBytes:  len(ev.Arguments),
		Allowed:         allow,
		DenyCode:        code,
		Reason:          r.scrub.clean(reason),
		At:              at,
		Decided:         time.Since(started),
	}
	r.receipt.ToolRequests = append(r.receipt.ToolRequests, record)

	msg := decisionMessage{
		Type:      MessageToolDecision,
		RequestID: ev.RequestID,
		Decision:  decisionAllow,
		Reason:    record.Reason,
	}
	if !allow {
		msg.Decision = decisionDeny
		msg.Code = code
	}
	return r.session.writeMessage(msg)
}

// decide applies the fixed authorization order. The grant is checked before
// the policy, so a permissive policy can never widen a run's boundary.
func (r *runner) decide(ctx context.Context, ev event) (bool, DenyCode, string) {
	if _, repeated := r.seen[ev.RequestID]; repeated {
		return false, DenyDuplicate, "request_id already used in this run"
	}
	r.seen[ev.RequestID] = struct{}{}

	if ev.Capability == "" {
		return false, DenyMalformed, "request names no capability"
	}
	if !ev.Capability.Known() {
		return false, DenyUnknownCapability, "capability is not one Babel defines"
	}
	if !r.job.Grant.Allows(ev.Capability) {
		return false, DenyNotGranted, "capability is outside this run's grant"
	}
	if !r.job.Grant.ExpiresAt.IsZero() && time.Now().After(r.job.Grant.ExpiresAt) {
		return false, DenyNotGranted, "the run's capability grant has expired"
	}
	if r.toolCount > r.limits.MaxToolRequests {
		return false, DenyLimit, "tool request budget exhausted"
	}

	decision := r.client.authorizer().Authorize(ctx, ToolRequest{
		JobID:      r.job.JobID,
		RunID:      r.job.RunID,
		Index:      r.toolCount,
		RequestID:  ev.RequestID,
		Capability: ev.Capability,
		Tool:       ev.Tool,
		Arguments:  ev.Arguments,
		Reason:     ev.Reason,
		Grant:      r.job.Grant,
	})
	if !decision.Allow {
		return false, DenyPolicy, decision.Reason
	}
	return true, "", decision.Reason
}

// handleResult records the run's output.
func (r *runner) handleResult(ev event, at time.Time) error {
	if !r.sawConfig {
		return fmt.Errorf("%w: result before the resolved configuration", ErrEventOrder)
	}
	if ev.Status != StatusOK && ev.Status != StatusPartial {
		return fmt.Errorf("%w: result status %q", ErrMalformedEvent, r.scrub.clean(ev.Status))
	}
	if ev.Resources != nil {
		observed := *ev.Resources
		r.receipt.Resources = &observed
	}
	r.terminal = MessageResult
	r.receipt.Result = &ResultRecord{
		Status:  ev.Status,
		Schema:  r.scrub.clean(ev.Schema),
		Payload: r.scrub.cleanJSON(ev.Payload),
		At:      at,
	}
	return nil
}

// handleWorkerError records the worker's own failure. It is terminal: the
// counterpart said it failed, and a later result would contradict that.
func (r *runner) handleWorkerError(ev event, at time.Time) error {
	r.terminal = MessageError
	r.workerErr = &WorkerError{
		Code:      r.scrub.clean(ev.Code),
		Message:   r.scrub.clean(ev.Message),
		Retryable: ev.Retryable,
	}
	r.receipt.Failure = &FailureRecord{
		Origin:    FailureWorker,
		Code:      r.workerErr.Code,
		Message:   r.workerErr.Message,
		Retryable: ev.Retryable,
		At:        at,
	}
	return nil
}

// finalize turns the supervision outcome into Run's error, in precedence
// order: a protocol or supervision failure, then a missing terminal event,
// then the worker's own reported error, then a self-contradicting exit status.
func (r *runner) finalize(fatal error) error {
	switch {
	case errors.Is(fatal, errDrained):
		// The child exited and left its stdout with a descendant. The stream
		// has ended; judge the run on what it delivered.
	case errors.Is(fatal, context.Canceled), errors.Is(fatal, context.DeadlineExceeded):
		// The bare context error says nothing about what was cancelled.
		return fmt.Errorf("worker run %s: %w", r.job.JobID, fatal)
	case fatal != nil:
		return fatal
	}
	if r.terminal == "" {
		return fmt.Errorf("%w: exit status %d", ErrNoResult, r.session.exitCode())
	}
	if r.terminal == MessageError {
		return r.workerErr
	}
	if code := r.session.exitCode(); code != 0 {
		return fmt.Errorf("%w: exit status %d", ErrDirtyExit, code)
	}
	return nil
}

// knownEventType reports whether t is a worker-to-Babel message this version
// defines.
func knownEventType(t string) bool {
	switch t {
	case MessageConfiguration, MessageProgress, MessageToolRequest, MessageResult, MessageError:
		return true
	}
	return false
}

// eventTime is the worker's timestamp, or Babel's observation time when the
// worker supplied none. Which one it is matters: a receipt built from worker
// clocks alone would be unfalsifiable.
func eventTime(ev event) time.Time {
	if ev.Time != nil {
		return ev.Time.UTC()
	}
	return time.Now().UTC()
}

// argumentsDigest fingerprints a tool argument blob. The arguments themselves
// are never recorded: they can carry private locators, and a worker echoing a
// credential into one must not be able to write it into Babel's durable audit
// record at all.
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
		{ErrProtocolMismatch, "protocol-mismatch"},
		{ErrVersionMismatch, "version-mismatch"},
		{ErrModeUnsupported, "mode-unsupported"},
		{ErrHandshakeTimeout, "handshake-timeout"},
		{ErrOversizedLine, "oversized-line"},
		{ErrMalformedEvent, "malformed-event"},
		{ErrUnknownEventType, "unknown-event-type"},
		{ErrSequence, "sequence-violation"},
		{ErrEventOrder, "event-order"},
		{ErrProfileMismatch, "profile-mismatch"},
		{ErrDuplicateResult, "duplicate-result"},
		{ErrEventAfterResult, "event-after-result"},
		{ErrResultAfterError, "result-after-error"},
		{ErrNoResult, "no-result"},
		{ErrWorkerStalled, "stalled"},
		{ErrWorkerLingered, "lingered"},
		{ErrDirtyExit, "dirty-exit"},
		{ErrEventBudget, "event-budget"},
		{ErrToolBudget, "tool-budget"},
		{ErrSecretDeclared, "secret-declared"},
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

// inbound is one line read from the worker, or the reason reading stopped.
type inbound struct {
	ev      event
	fields  map[string]json.RawMessage
	unknown []string
	err     error
}

// session is one supervised worker process: its pipes, its reader goroutines,
// and its reaping.
//
// The pipes are created here rather than with exec.Cmd's StdoutPipe helpers so
// that Babel owns both ends. Closing a read end is then the deterministic way
// to release a blocked reader, with none of the "do not call Wait before reads
// complete" hazard the helper pipes carry — which matters because the whole
// point is to survive a worker that leaves a descendant holding the pipe.
type session struct {
	cmd     *exec.Cmd
	pgid    int
	limits  Limits
	scrub   scrubber
	stdinW  *os.File
	stdoutR *os.File
	stderrR *os.File

	inbound chan inbound
	stop    chan struct{}
	reaped  chan struct{}
	tail    *tail
	wg      sync.WaitGroup

	// raw is Config.rawTranscript: nil in every production run, and the
	// conformance suite's unscrubbed view of what the worker wrote when the
	// credential obligation is grading one.
	raw *tail

	killOnce sync.Once
	downOnce sync.Once
}

// start launches the worker and its supervision goroutines.
func (c *Client) start(ctx context.Context, limits Limits, scrub scrubber) (*session, error) {
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

	cmd := exec.Command(c.cfg.Binary, c.cfg.Args...)
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
		scrub:   scrub,
		stdinW:  inW,
		stdoutR: outR,
		stderrR: errR,
		inbound: make(chan inbound),
		stop:    make(chan struct{}),
		reaped:  make(chan struct{}),
		tail:    &tail{limit: limits.StderrTailBytes},
		raw:     c.cfg.rawTranscript,
	}

	s.wg.Add(3)
	go s.readEvents()
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

// readEvents parses the worker's stdout into inbound values. Every stop is
// reported exactly once: EOF, an oversized line, a malformed line, or a read
// failure.
//
// The raw line is offered to the conformance transcript before it is decoded,
// which is the only place the worker's own bytes exist unaltered. Parsing is
// unaffected: the tee is nil in every production run, and reading a line the
// grader also observed is the same work either way.
func (s *session) readEvents() {
	defer s.wg.Done()
	reader := bufio.NewReaderSize(s.stdoutR, readBufferSize)
	for {
		line, err := readLine(reader, s.limits.MaxLineBytes)
		if len(bytes.TrimSpace(line)) > 0 {
			if s.raw != nil {
				s.raw.writeLine(string(line))
			}
			var ev event
			fields, unknown, decodeErr := decode(line, &ev)
			if decodeErr != nil {
				s.deliver(inbound{err: fmt.Errorf("%w: %s", ErrMalformedEvent, s.scrub.clean(decodeErr.Error()))})
				return
			}
			if !s.deliver(inbound{ev: ev, fields: fields, unknown: unknown}) {
				return
			}
		}
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, io.EOF):
			s.deliver(inbound{err: io.EOF})
		case errors.Is(err, ErrOversizedLine):
			s.deliver(inbound{err: err})
		case errors.Is(err, os.ErrClosed):
			// Teardown closed the pipe; nobody is listening any more.
		default:
			s.deliver(inbound{err: fmt.Errorf("worker: reading events: %w", err)})
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

// readDiagnostics drains the worker's stderr into the bounded tail and the
// optional diagnostics sink. It is never parsed: stderr carries the worker's
// own logging, and treating it as protocol would let a log line steer a run.
//
// Each line is bounded before it is retained. A worker that writes a gigabyte
// without a newline is misbehaving, and reading that into memory to log it
// would let it take Babel down.
func (s *session) readDiagnostics(sink io.Writer) {
	defer s.wg.Done()
	reader := bufio.NewReaderSize(s.stderrR, readBufferSize)
	for {
		line, truncated, err := readDiagnosticLine(reader, stderrLineLimit)
		if trimmed := strings.TrimRight(string(line), "\r\n"); trimmed != "" {
			// Before scrubbing: the conformance transcript is what the worker
			// wrote, and stderr is a channel a careless worker leaks through.
			if s.raw != nil {
				s.raw.writeLine(trimmed)
			}
			cleaned := s.scrub.clean(trimmed)
			if truncated {
				cleaned += " [truncated]"
			}
			s.tail.writeLine(cleaned)
			if sink != nil {
				fmt.Fprintf(sink, "worker: %s\n", cleaned)
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

// writeMessage encodes one Babel-to-worker message as a single line.
func (s *session) writeMessage(msg any) error {
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("worker: encoding message: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := s.stdinW.Write(encoded); err != nil {
		return fmt.Errorf("worker: writing to worker stdin: %w", err)
	}
	return nil
}

// next waits for the next inbound value, for at most budget. A child that
// exits mid-wait does not end the wait immediately: lines it already wrote may
// still be in the pipe, so the budget is replaced by the shorter drain grace.
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

// handshake performs version negotiation for one mode and returns the
// accepted hello. On refusal it writes the refusal so the worker learns why,
// and no job material is ever written.
func (s *session) handshake(ctx context.Context, mode string, versions []int) (helloMessage, int, error) {
	in, err := s.next(ctx, s.limits.HandshakeTimeout)
	if err != nil {
		if errors.Is(err, errTimeout) {
			return helloMessage{}, 0, fmt.Errorf("%w: no hello within %s", ErrHandshakeTimeout, s.limits.HandshakeTimeout)
		}
		return helloMessage{}, 0, err
	}
	if in.err != nil {
		return helloMessage{}, 0, s.classifyStreamError(in.err)
	}

	var hello helloMessage
	if _, _, err := decode(in.rawOrEncoded(), &hello); err != nil {
		return helloMessage{}, 0, fmt.Errorf("%w: %s", ErrMalformedEvent, s.scrub.clean(err.Error()))
	}
	if hello.Type != MessageHello {
		return hello, 0, fmt.Errorf("%w: first line was %q, not a hello",
			ErrProtocolMismatch, s.scrub.clean(hello.Type))
	}
	if hello.Protocol != ProtocolName {
		return hello, 0, errors.Join(fmt.Errorf("%w: worker speaks %q, Babel speaks %q",
			ErrProtocolMismatch, s.scrub.clean(hello.Protocol), ProtocolName),
			s.refuse("unrecognized protocol", versions))
	}
	version, ok := negotiate(versions, hello.Versions)
	if !ok {
		return hello, 0, errors.Join(fmt.Errorf("%w: worker offers %v, Babel offers %v",
			ErrVersionMismatch, hello.Versions, versions),
			s.refuse("no mutually supported protocol version", versions))
	}
	if !containsString(hello.Modes, mode) {
		return hello, version, errors.Join(fmt.Errorf("%w: worker offers %v, Babel needs %q",
			ErrModeUnsupported, hello.Modes, mode),
			s.refuse("mode "+mode+" unsupported", versions))
	}

	if err := s.writeMessage(acceptMessage{
		Type:     MessageAccept,
		Protocol: ProtocolName,
		Version:  version,
		Mode:     mode,
		Limits:   s.limits.onWire(),
	}); err != nil {
		return hello, version, err
	}
	return hello, version, nil
}

// refuse tells a rejected worker why, closes its stdin, and gives it the exit
// grace to leave on its own. It returns ErrWorkerLingered when it does not.
//
// The wait is part of the contract rather than politeness: a refused worker
// must exit, and killing it the instant the refusal is written would make that
// obligation unobservable — Babel would never learn the difference between a
// worker that exits and one that hangs. The refusal reason is joined with this
// result, so a caller matching ErrVersionMismatch still matches while
// Conformance can also see that the worker refused to leave.
//
// A write failure is discarded: the caller is already returning the refusal
// reason, and a worker that cannot be told is killed by teardown anyway.
func (s *session) refuse(reason string, versions []int) error {
	_ = s.writeMessage(refuseMessage{
		Type:      MessageRefuse,
		Protocol:  ProtocolName,
		Reason:    reason,
		Supported: versions,
	})
	_ = s.stdinW.Close()
	return s.awaitExit(s.limits.ExitGrace)
}

// classifyStreamError turns a reader failure during a synchronous wait into
// the sentinel that describes it.
func (s *session) classifyStreamError(err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: exit status %d", ErrNoResult, s.exitCode())
	}
	return err
}

// wrapWait translates a wait failure: an elapsed budget means the worker went
// quiet, unless it has already exited, in which case it simply never
// answered.
func (s *session) wrapWait(err error) error {
	if !errors.Is(err, errTimeout) {
		return err
	}
	if s.hasExited() {
		return fmt.Errorf("%w: exit status %d", ErrNoResult, s.exitCode())
	}
	return fmt.Errorf("%w: no event for %s", ErrWorkerStalled, s.limits.IdleTimeout)
}

// awaitExit waits for the process to exit within grace, killing the tree when
// it does not.
func (s *session) awaitExit(grace time.Duration) error {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.reaped:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w: still running %s after answering", ErrWorkerLingered, grace)
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
// guarantee whole — a sandbox the worker spawned is in that group, and killing
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

// rawOrEncoded re-encodes a decoded inbound so a second, differently-shaped
// decode can run over the same line. Only the handshake needs it, and only
// once per process, so re-encoding beats carrying the raw bytes through every
// event.
func (in inbound) rawOrEncoded() []byte {
	encoded, err := json.Marshal(in.fields)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// readLine reads one newline-terminated line, enforcing max on the payload.
// It exists instead of bufio.Scanner because Scanner only reports ErrTooLong
// once a token exceeds its *buffer*, so a small configured maximum would not
// be enforced at all.
func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, chunk...)
			if len(line) > max {
				return nil, fmt.Errorf("%w: over %d bytes", ErrOversizedLine, max)
			}
			continue
		}
		line = append(line, chunk...)
		if err != nil {
			return line, err
		}
		if len(line)-1 > max {
			return nil, fmt.Errorf("%w: %d bytes over a %d byte limit", ErrOversizedLine, len(line)-1, max)
		}
		return line[:len(line)-1], nil
	}
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

// scrubber removes job secrets from worker-controlled text. It is Babel's
// defence, not the worker's obligation: a worker must not echo the broker
// credential, and a receipt must not carry it even when the worker does.
type scrubber struct {
	secrets []string
}

// newScrubber keeps the values long enough to be scrubbed safely.
func newScrubber(values []string) scrubber {
	var kept []string
	for _, value := range values {
		if len(value) >= minSecretLength {
			kept = append(kept, value)
		}
	}
	return scrubber{secrets: kept}
}

// clean replaces every secret occurrence in text.
func (s scrubber) clean(text string) string {
	if len(s.secrets) == 0 || text == "" {
		return text
	}
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, redactedMarker)
	}
	return text
}

// cleanMap scrubs a metadata map's values, returning a copy so the caller's
// map is never aliased into a receipt.
func (s scrubber) cleanMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(values))
	for key, value := range values {
		cleaned[key] = s.clean(value)
	}
	return cleaned
}

// cleanJSON scrubs a raw JSON payload textually. Babel-issued run tokens are
// URL-safe base64, so their JSON encoding is byte-identical to their value and
// a byte replacement cannot straddle an escape.
func (s scrubber) cleanJSON(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 || len(s.secrets) == 0 {
		return payload
	}
	cleaned := payload
	for _, secret := range s.secrets {
		cleaned = bytes.ReplaceAll(cleaned, []byte(secret), []byte(redactedMarker))
	}
	return cleaned
}

// tail keeps at most the last limit bytes of a worker-written stream, so a
// runaway child cannot balloon a receipt, an error message or the conformance
// suite's raw transcript. The bound is the whole point: it is the one idiom
// this package uses for retaining anything a worker controls.
type tail struct {
	mu      sync.Mutex
	limit   int
	buf     []byte
	dropped bool
}

// writeLine appends one line. The caller decides whether it has been scrubbed:
// the receipt's tail is given cleaned lines, the conformance transcript is
// given the worker's own bytes.
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

// discard drops what was retained. The conformance transcript holds a
// credential in the clear, so the obligation that captured it ends by
// releasing it rather than leaving it reachable for the rest of the process.
func (t *tail) discard() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = nil
	t.dropped = false
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
