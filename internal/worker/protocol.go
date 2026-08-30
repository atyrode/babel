// Package worker implements Babel's side of the Code analysis-worker
// protocol — the boundary SPEC.md §2.6 draws between Babel's control plane
// and Code, which owns provider/model/thinking profiles and launches OMP as
// the investigator (§6.5).
//
// Code does not implement this protocol yet. This package is therefore two
// things at once: Babel's client, and the executable definition of the
// contract. Conformance drives a candidate worker binary through every
// obligation listed below, and a worker that passes it is one Babel can
// supervise. Because the two programs are developed separately, nothing here
// assumes the counterpart is well-behaved: every version, every line, every
// ordering rule and every process in the tree is checked, and each way the
// counterpart can fail has its own error.
//
// # Transport
//
// Newline-delimited JSON objects in both directions. Worker to Babel on the
// child's stdout; Babel to worker on the child's stdin. The child's stderr is
// diagnostics only: it is captured as a bounded tail for error reporting and
// never parsed. One message per line, one line per message, no framing beyond
// the newline. A line longer than Limits.MaxLineBytes is a protocol violation
// rather than a message worth parsing.
//
// The job document travels on stdin and nowhere else. It carries the
// run-scoped evidence-broker credential, so it must not reach argv or the
// child's environment where a process listing would expose it — the same rule
// internal/restic follows for the repository password.
//
// # Handshake
//
// The worker speaks first, before reading anything, so Babel can refuse an
// incompatible counterpart before any job material is written to it:
//
//	{"type":"hello","protocol":"babel.analysis-worker","versions":[1],
//	 "modes":["configure","worker"],
//	 "worker":{"name":"code","version":"1.2.3"}}
//
// Babel replies with exactly one of:
//
//	{"type":"accept","protocol":"babel.analysis-worker","version":1,
//	 "mode":"worker",
//	 "limits":{"max_line_bytes":1048576,"max_events":100000,
//	           "max_tool_requests":1024,"idle_seconds":120,
//	           "exit_grace_seconds":5}}
//
//	{"type":"refuse","protocol":"babel.analysis-worker",
//	 "reason":"no mutually supported protocol version","supported":[1]}
//
// Version negotiation rule: Babel selects the highest version present in both
// its own supported set and the worker's "versions" array. An empty
// intersection, a "protocol" value that is not ProtocolName, or a "mode" the
// worker did not advertise is a refusal — and the refusal is written, not just
// returned, so the worker learns why and exits. A refused worker must exit
// without emitting further events.
//
// Unknown fields inside a known version are never fatal in either direction.
// Babel records the names it did not recognize (Receipt.UnknownFields) and
// preserves the raw values where they have product value
// (Configuration.Extra); a worker must likewise ignore fields it does not know
// in accept, job and tool-decision.
//
// # Configuration-only mode
//
// Mode "configure" opens Code's own dials, saves the result under Code's
// ownership, and exits without launching OMP. It emits exactly one message and
// then exits 0:
//
//	{"type":"configuration","profile":{"id":"p-1","revision":4},
//	 "privacy":{"disclosure":"local","redaction_required":false},
//	 "cost":{"currency":"USD","input_per_1k":0,"output_per_1k":0,
//	         "estimated_run":0},
//	 "capabilities":["corpus-search","repo-read"],
//	 "metadata":{"provider":"local","model":"m","thinking":"high"}}
//
// Babel persists only the profile reference and this non-secret metadata.
// A metadata key whose name looks like a credential is refused outright
// (ErrSecretDeclared): the boundary is that credentials never cross it, so a
// worker offering one is a broken worker, not an inconvenience to filter.
//
// # Worker mode
//
// Babel writes one job document after accept:
//
//	{"type":"job","job_id":"j-1","run_id":"r-1",
//	 "profile":{"id":"p-1","revision":4},
//	 "recipes":[{"id":"outcome-integrity","version":1}],
//	 "grant":{"capabilities":["corpus-search"],"disclosure":"local",
//	          "expires":"2026-08-29T12:00:00Z"},
//	 "sources":[{"kind":"session","selector":"omp/…","digest":"sha256:…",
//	             "snapshot":"…"}],
//	 "broker":{"endpoint":"http://127.0.0.1:0","token":"…"},
//	 "params":{"…":"…"}}
//
// The worker then streams events on stdout. Every event carries "seq",
// strictly increasing from 1 across all event types, so a dropped or reordered
// stream is detectable rather than merely wrong. The first event must be a
// "configuration" reporting the profile the worker actually resolved — Babel
// requires it because §6.5 makes resolved provider metadata part of the
// receipt, and it must name the profile the job named (ErrProfileMismatch).
//
// In worker mode that configuration must also declare the sandbox the worker
// provides. Babel does not implement one — Code owns the disposable sandbox and
// credential isolation (SPEC.md §2.6, decision 53) — so the declaration is what
// Babel holds the worker to before anything executes, and a declaration short
// of the run's requirement is refused (ErrContainment):
//
//	{"type":"configuration","seq":1,"time":"…",
//	 "profile":{"id":"p-1","revision":4},
//	 "privacy":{"disclosure":"local","redaction_required":false},
//	 "cost":{…},"capabilities":["corpus-search"],"metadata":{…},
//	 "containment":{"backend":"…","filesystem_isolation":true,
//	                "network_default_deny":true,"resource_ceilings":true,
//	                "disposable":true,"escape":"what this does not contain"}}
//
// The "escape" field is required and may not be empty. A sandbox whose author
// claims no residual risk has not been examined, and §10 requires uncertainty to
// stay visible rather than be rounded to zero. Babel records the declaration in
// the receipt so a reviewer sees which boundary the evidence came from.
//
//	{"type":"progress","seq":2,"time":"…","stage":"discover",
//	 "message":"…","fraction":0.25,
//	 "resources":{"cpu_seconds":1.5,"max_rss_bytes":1048576,
//	              "sandbox_bytes_written":0,"tool_calls":1}}
//
//	{"type":"tool-request","seq":3,"time":"…","request_id":"t-1",
//	 "capability":"corpus-search","tool":"search","arguments":{…},
//	 "reason":"…"}
//
//	{"type":"result","seq":9,"time":"…","status":"ok",
//	 "schema":"babel.analysis-result/1","payload":{…},"resources":{…}}
//
//	{"type":"error","seq":9,"time":"…","code":"profile-unavailable",
//	 "message":"…","retryable":false}
//
// Babel answers every tool-request on stdin and the worker must block until it
// does:
//
//	{"type":"tool-decision","request_id":"t-1","decision":"allow"}
//	{"type":"tool-decision","request_id":"t-1","decision":"deny",
//	 "code":"not-granted","reason":"…"}
//
// A denial is not a termination. The worker adapts and keeps working, and it
// must still deliver a terminal event.
//
// "result" and "error" are both terminal: exactly one of them per run,
// nothing after it, and the worker exits promptly — 0 after a result, any
// status after an error, since Babel owns the run's final status either way.
//
// # Authorization
//
// Babel decides every tool-request, in this fixed order, so that the run's
// capability grant is a boundary rather than a hint:
//
//  1. an empty or repeated request_id is a protocol violation / a
//     "duplicate-request" denial;
//  2. a capability outside Grant.Capabilities is denied
//     ("not-granted", or "unknown-capability" when the name is not one Babel
//     defines) — before the policy is consulted, so a permissive policy can
//     never widen a grant;
//  3. a request past Limits.MaxToolRequests is denied ("limit"); and
//  4. only then does the injected Authorizer decide ("policy" on denial).
//
// Tool arguments are given to the Authorizer and never recorded. The receipt
// keeps their digest and size instead, so a worker that echoes a credential or
// a private locator into an argument cannot write it into Babel's durable
// audit record at all.
//
// # Obligations Conformance enforces
//
// See Conformance for the executable list; its subtest names are the contract
// items a candidate implementation must satisfy.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProtocolName identifies this protocol on the wire. A counterpart that
// declares anything else is a different program, not an older version of this
// one, so the mismatch is reported separately from a version mismatch.
const ProtocolName = "babel.analysis-worker"

// ProtocolVersion is the protocol revision this package implements. Semantic
// changes to any message increment it; adding an optional field does not,
// because unknown fields are ignored by contract in both directions.
const ProtocolVersion = 1

// Message types on the wire. Babel writes accept, refuse, job and
// tool-decision; the worker writes hello, configuration, progress,
// tool-request, result and error.
const (
	MessageHello         = "hello"
	MessageAccept        = "accept"
	MessageRefuse        = "refuse"
	MessageJob           = "job"
	MessageToolDecision  = "tool-decision"
	MessageConfiguration = "configuration"
	MessageProgress      = "progress"
	MessageToolRequest   = "tool-request"
	MessageResult        = "result"
	MessageError         = "error"
)

// Modes a worker can be accepted into. ModeConfigure returns a saved profile
// reference and exits without launching OMP; ModeWorker runs one analysis job.
const (
	ModeConfigure = "configure"
	ModeWorker    = "worker"
)

// Result statuses. StatusPartial reports work that stopped short of the job's
// scope but produced usable output — a finite run deferring its frontier
// (SPEC.md §5.2) rather than a failure.
const (
	StatusOK      = "ok"
	StatusPartial = "partial"
)

// Disclosure classes a grant can carry. The class is fixed before material is
// sent (SPEC.md §3), so it travels in the job rather than being negotiated.
const (
	DisclosureLocal  = "local"
	DisclosureHosted = "hosted"
)

// DefaultVersions is the protocol version set Babel offers. It is a function
// rather than a package variable so no caller can mutate the negotiation
// baseline of every other client in the process.
func DefaultVersions() []int { return []int{ProtocolVersion} }

// Capability names one evidence or execution facility a run may grant. The
// set matches the cookbook's `capabilities` front matter (SPEC.md §5.1) plus
// brokered public research (§2.6).
type Capability string

// Capabilities Babel defines. A request naming anything else is denied as
// unknown rather than passed to the policy, because a policy cannot
// meaningfully reason about a capability Babel has no boundary for.
const (
	CapabilityCorpusSearch   Capability = "corpus-search"
	CapabilityRepoRead       Capability = "repo-read"
	CapabilitySandboxExec    Capability = "sandbox-exec"
	CapabilityPublicResearch Capability = "public-research"
)

// Known reports whether c is a capability Babel defines.
func (c Capability) Known() bool {
	switch c {
	case CapabilityCorpusSearch, CapabilityRepoRead, CapabilitySandboxExec, CapabilityPublicResearch:
		return true
	}
	return false
}

// ProfileRef identifies one Code-owned analysis profile. Babel stores the
// reference and never the provider configuration behind it (SPEC.md §2.6).
type ProfileRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

// String renders the reference for diagnostics as id@revision.
func (p ProfileRef) String() string { return fmt.Sprintf("%s@%d", p.ID, p.Revision) }

// RecipeRef identifies one cookbook asset at a version. Semantic recipe
// changes increment the version (SPEC.md §5.1), so the pair is what a receipt
// must record.
type RecipeRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Source is one approved input the run may read. Selector and digest identify
// an immutable capture; Snapshot names the restic snapshot it came from when
// the source is archived material.
type Source struct {
	Kind     string `json:"kind"`
	Selector string `json:"selector"`
	Digest   string `json:"digest,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
}

// Grant is the run's capability boundary, fixed before work starts. It is
// deliberately separate from the Authorizer: the policy may narrow the grant
// but can never widen it.
type Grant struct {
	Capabilities []Capability
	Disclosure   string
	ExpiresAt    time.Time
}

// Allows reports whether c is inside the grant.
func (g Grant) Allows(c Capability) bool {
	for _, have := range g.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// Broker locates Babel's capability-gated evidence API for this run. Token is
// a run-scoped credential Babel issues, and it is the one secret the job
// carries: it reaches the worker on stdin only, is scrubbed out of every
// diagnostic Babel emits, and never enters a receipt.
type Broker struct {
	Endpoint string
	Token    string
}

// Job is one analysis job. Extra carries forward-compatible top-level fields
// a newer Babel may add; they are merged into the encoded document.
type Job struct {
	JobID   string
	RunID   string
	Profile ProfileRef
	Recipes []RecipeRef
	Grant   Grant
	Sources []Source
	Broker  Broker
	Params  map[string]string
	Extra   map[string]json.RawMessage
}

// secrets lists the values in j that must never appear in a receipt, an error
// or a diagnostic line.
func (j Job) secrets() []string {
	if j.Broker.Token == "" {
		return nil
	}
	return []string{j.Broker.Token}
}

// jobWire is the encoded form of a Job. It exists separately so the exported
// type can hold Go values (time.Time, Capability) while the wire stays the
// documented JSON shape.
type jobWire struct {
	Type     string            `json:"type"`
	JobID    string            `json:"job_id"`
	RunID    string            `json:"run_id"`
	Profile  ProfileRef        `json:"profile"`
	Recipes  []RecipeRef       `json:"recipes,omitempty"`
	Grant    grantWire         `json:"grant"`
	Sources  []Source          `json:"sources,omitempty"`
	Broker   *brokerWire       `json:"broker,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Protocol string            `json:"protocol"`
}

type grantWire struct {
	Capabilities []Capability `json:"capabilities"`
	Disclosure   string       `json:"disclosure"`
	Expires      *time.Time   `json:"expires,omitempty"`
}

type brokerWire struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

// MarshalJSON encodes the job document, merging Extra's unknown-to-this-build
// fields at the top level. A collision with a documented field is an error
// rather than a silent overwrite: a caller redefining "grant" through Extra
// would be quietly rewriting the capability boundary.
func (j Job) MarshalJSON() ([]byte, error) {
	w := jobWire{
		Type:     MessageJob,
		Protocol: ProtocolName,
		JobID:    j.JobID,
		RunID:    j.RunID,
		Profile:  j.Profile,
		Recipes:  j.Recipes,
		Grant: grantWire{
			Capabilities: j.Grant.Capabilities,
			Disclosure:   j.Grant.Disclosure,
		},
		Sources: j.Sources,
		Params:  j.Params,
	}
	if w.Grant.Capabilities == nil {
		w.Grant.Capabilities = []Capability{}
	}
	if !j.Grant.ExpiresAt.IsZero() {
		expires := j.Grant.ExpiresAt.UTC()
		w.Grant.Expires = &expires
	}
	if j.Broker.Endpoint != "" || j.Broker.Token != "" {
		w.Broker = &brokerWire{Endpoint: j.Broker.Endpoint, Token: j.Broker.Token}
	}
	encoded, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	if len(j.Extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for name, value := range j.Extra {
		if _, taken := merged[name]; taken {
			return nil, fmt.Errorf("job: extra field %q collides with a documented field", name)
		}
		merged[name] = value
	}
	return json.Marshal(merged)
}

// helloMessage is the worker's opening line.
type helloMessage struct {
	Type     string   `json:"type"`
	Protocol string   `json:"protocol"`
	Versions []int    `json:"versions"`
	Modes    []string `json:"modes"`
	Worker   Identity `json:"worker"`
}

// Identity is the worker's non-secret self-description, recorded in receipts
// so a run can be attributed to a build.
type Identity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// acceptMessage names the negotiated version, the mode, and the transport
// budgets the worker must respect.
type acceptMessage struct {
	Type     string       `json:"type"`
	Protocol string       `json:"protocol"`
	Version  int          `json:"version"`
	Mode     string       `json:"mode"`
	Limits   limitsOnWire `json:"limits"`
}

type limitsOnWire struct {
	MaxLineBytes    int     `json:"max_line_bytes"`
	MaxEvents       int     `json:"max_events"`
	MaxToolRequests int     `json:"max_tool_requests"`
	IdleSeconds     float64 `json:"idle_seconds"`
	ExitGraceSecs   float64 `json:"exit_grace_seconds"`
}

// refuseMessage tells a rejected worker why, so it exits instead of waiting
// for a job that will never arrive.
type refuseMessage struct {
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	Reason    string `json:"reason"`
	Supported []int  `json:"supported"`
}

// decisionMessage is Babel's answer to one tool-request.
type decisionMessage struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	Decision  string   `json:"decision"`
	Code      DenyCode `json:"code,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

// Decision values on the wire.
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
)

// DenyCode explains a denial in a form both the worker and a later reviewer
// can act on.
type DenyCode string

// Denial reasons. They are ordered by the check that produces them, and the
// order is part of the contract: not-granted precedes policy.
const (
	DenyMalformed         DenyCode = "malformed-request"
	DenyDuplicate         DenyCode = "duplicate-request"
	DenyUnknownCapability DenyCode = "unknown-capability"
	DenyNotGranted        DenyCode = "not-granted"
	DenyLimit             DenyCode = "limit"
	DenyPolicy            DenyCode = "policy"
)

// event is one worker-to-Babel line, decoded as a flat union: fields
// belonging to other message types stay zero. The same shape restic's --json
// protocol is read with (internal/restic), for the same reason — one decode
// per line, no type-dispatch allocation.
type event struct {
	Type string     `json:"type"`
	Seq  int        `json:"seq"`
	Time *time.Time `json:"time"`

	// progress
	Stage    string  `json:"stage"`
	Fraction float64 `json:"fraction"`

	// progress, error
	Message string `json:"message"`

	// progress, result
	Resources *Resources `json:"resources"`

	// tool-request
	RequestID  string          `json:"request_id"`
	Capability Capability      `json:"capability"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Reason     string          `json:"reason"`

	// result
	Status  string          `json:"status"`
	Schema  string          `json:"schema"`
	Payload json.RawMessage `json:"payload"`

	// error
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`

	// configuration
	Profile      ProfileRef        `json:"profile"`
	Privacy      Privacy           `json:"privacy"`
	Cost         Cost              `json:"cost"`
	Capabilities []Capability      `json:"capabilities"`
	Metadata     map[string]string `json:"metadata"`
	Containment  Containment       `json:"containment"`
}

// Resources is the worker's self-reported resource use. It is a pointer
// wherever it is recorded: a worker that reports nothing has unknown resource
// use, which is not the same claim as zero.
type Resources struct {
	CPUSeconds          float64 `json:"cpu_seconds"`
	MaxRSSBytes         int64   `json:"max_rss_bytes"`
	SandboxBytesWritten int64   `json:"sandbox_bytes_written"`
	ToolCalls           int     `json:"tool_calls"`
}

// Privacy is the profile's disclosure class and redaction requirement — the
// fields §3 requires Babel to show before material is sent.
type Privacy struct {
	Disclosure        string `json:"disclosure"`
	RedactionRequired bool   `json:"redaction_required"`
}

// Cost is the profile's non-secret cost metadata. Babel records it to support
// cost guards; it is the profile's own estimate, never a measurement.
type Cost struct {
	Currency     string  `json:"currency"`
	InputPer1K   float64 `json:"input_per_1k"`
	OutputPer1K  float64 `json:"output_per_1k"`
	EstimatedRun float64 `json:"estimated_run"`
}

// Containment is the sandbox a worker declares it provides. Babel does not
// implement the sandbox — Code owns it, because Code owns the profile, the
// provider credential, and the OMP controller (SPEC §2.6, decision 53) — so
// Babel's containment is only as good as this declaration plus the obligations
// the conformance suite checks. That is exactly why the declaration is
// mandatory rather than advisory: launching analysis into an unspecified
// sandbox would be trusting a boundary nobody stated.
//
// Every field is a claim by the worker about itself. Babel cannot verify a
// claim from outside the process, and does not pretend to: it refuses a worker
// whose declaration falls short of the run's requirement, and records the
// declaration in the receipt so a later reviewer sees which boundary the
// evidence was produced behind.
type Containment struct {
	// Backend names the mechanism, for the receipt and for an operator
	// deciding whether to trust it. Free-form because the set is Code's to
	// grow, but empty is refused: an unnamed mechanism cannot be assessed.
	Backend string `json:"backend"`
	// FilesystemIsolation reports that the worker's writes cannot reach the
	// host filesystem outside paths the grant named.
	FilesystemIsolation bool `json:"filesystem_isolation"`
	// NetworkDefaultDeny reports that egress is denied unless a granted
	// capability opens it. Public research reaches the network through
	// Babel's broker, never from inside the sandbox, so a worker that cannot
	// claim this is not eligible for a run that discloses anything.
	NetworkDefaultDeny bool `json:"network_default_deny"`
	// ResourceCeilings reports that CPU, memory, and disk are bounded, so a
	// run cannot exhaust the machine that hosts the archive.
	ResourceCeilings bool `json:"resource_ceilings"`
	// Disposable reports that the execution environment is destroyed at
	// teardown, so nothing a run wrote survives into the next one.
	Disposable bool `json:"disposable"`
	// Escape is the worker's own statement of what it does not contain. It is
	// required and may not be empty: a sandbox whose author claims no
	// residual risk has not been thought about, and §10 requires uncertainty
	// to stay visible rather than be rounded to zero.
	Escape string `json:"escape"`
}

// Requirement is the containment a run demands. Babel refuses to launch a
// worker that declares less, before any job material reaches it.
type Requirement struct {
	FilesystemIsolation bool
	NetworkDefaultDeny  bool
	ResourceCeilings    bool
	Disposable          bool
}

// SandboxedRun is the requirement every exploration run uses. It is the strict
// setting deliberately: a weaker default would silently become the norm, and
// the operator who wants to relax it should have to say so per run.
func SandboxedRun() Requirement {
	return Requirement{
		FilesystemIsolation: true,
		NetworkDefaultDeny:  true,
		ResourceCeilings:    true,
		Disposable:          true,
	}
}

// Satisfies reports whether a declaration meets a requirement, naming every
// shortfall rather than the first, so an operator sees the whole gap in one
// message instead of fixing them one launch at a time.
func (c Containment) Satisfies(r Requirement) error {
	if strings.TrimSpace(c.Backend) == "" {
		return fmt.Errorf("%w: worker declared no sandbox backend", ErrContainment)
	}
	if strings.TrimSpace(c.Escape) == "" {
		return fmt.Errorf("%w: worker declared no escape assumption for backend %q", ErrContainment, c.Backend)
	}
	var missing []string
	if r.FilesystemIsolation && !c.FilesystemIsolation {
		missing = append(missing, "filesystem isolation")
	}
	if r.NetworkDefaultDeny && !c.NetworkDefaultDeny {
		missing = append(missing, "network default-deny")
	}
	if r.ResourceCeilings && !c.ResourceCeilings {
		missing = append(missing, "resource ceilings")
	}
	if r.Disposable && !c.Disposable {
		missing = append(missing, "disposable environment")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: backend %q does not provide %s",
			ErrContainment, c.Backend, strings.Join(missing, ", "))
	}
	return nil
}

// Configuration is a resolved Code profile: the reference Babel persists plus
// the non-secret metadata a receipt and a consent prompt need.
type Configuration struct {
	Profile         ProfileRef
	Privacy         Privacy
	Cost            Cost
	Capabilities    []Capability
	Metadata        map[string]string
	Worker          Identity
	ProtocolVersion int

	// Containment is the sandbox the worker declares. It is empty in
	// configuration-only mode, where nothing executes, and required in worker
	// mode, where something does.
	Containment Containment

	// Extra holds fields this build does not know, preserved so a newer Code
	// can carry profile metadata through an older Babel without loss.
	Extra map[string]json.RawMessage

	// Unknown names the fields Extra holds, sorted, for receipts and
	// diagnostics.
	Unknown []string
}

// secretKeyMarkers are the substrings that make a metadata key credential
// shaped. Matching on substrings rather than exact names is deliberate: the
// rule should not be defeated by naming a field "openai_api_key_value".
var secretKeyMarkers = []string{
	"api_key", "apikey", "authorization", "bearer", "credential",
	"passwd", "password", "private_key", "secret", "token",
}

// validateMetadata refuses metadata that declares a credential. Babel stores
// only non-secret execution metadata (SPEC.md §2.6); a worker offering a
// secret has misunderstood the boundary, and filtering it silently would hide
// that from whoever has to fix it.
func validateMetadata(metadata map[string]string) error {
	names := make([]string, 0, len(metadata))
	for name := range metadata {
		lower := strings.ToLower(name)
		for _, marker := range secretKeyMarkers {
			if strings.Contains(lower, marker) {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("%w: metadata key(s) %s", ErrSecretDeclared, strings.Join(names, ", "))
}

// negotiate selects the highest version offered by both sides.
func negotiate(local, remote []int) (int, bool) {
	best, found := 0, false
	for _, l := range local {
		for _, r := range remote {
			if l == r && l > best {
				best, found = l, true
			}
		}
	}
	return best, found
}

// WorkerError reports a worker's own "error" event: the run reached the
// counterpart and the counterpart failed, which is a different fact from
// Babel refusing or the protocol breaking.
type WorkerError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *WorkerError) Error() string {
	if e.Code == "" {
		return "worker reported an error: " + e.Message
	}
	return fmt.Sprintf("worker reported %s: %s", e.Code, e.Message)
}

// Is lets callers match any worker-reported failure with
// errors.Is(err, ErrWorkerReported) without knowing the code.
func (e *WorkerError) Is(target error) bool { return target == ErrWorkerReported }

// Sentinel errors. Every way the counterpart or the protocol can fail has its
// own error, because "the worker misbehaved" is not something an operator can
// act on. Callers match them with errors.Is.
var (
	// ErrProtocolMismatch reports a counterpart that is not speaking this
	// protocol at all: a wrong "protocol" value, or a first line that is not
	// a hello.
	ErrProtocolMismatch = errors.New("worker: protocol mismatch")

	// ErrVersionMismatch reports an empty intersection between Babel's
	// supported versions and the worker's. The worker is refused on the wire
	// before any job material is written.
	ErrVersionMismatch = errors.New("worker: no mutually supported protocol version")

	// ErrModeUnsupported reports a worker that does not offer the mode this
	// call needs.
	ErrModeUnsupported = errors.New("worker: mode unsupported")

	// ErrHandshakeTimeout reports a worker that produced no hello within the
	// handshake budget. Distinct from ErrWorkerStalled: nothing was
	// negotiated, so no job material was at risk.
	ErrHandshakeTimeout = errors.New("worker: handshake timed out")

	// ErrMalformedEvent reports a stdout line that is not a decodable JSON
	// object, or one missing a field the protocol requires to answer it.
	ErrMalformedEvent = errors.New("worker: malformed event")

	// ErrUnknownEventType reports a well-formed line whose "type" this
	// protocol version does not define. Unknown fields are tolerated;
	// unknown message types are not, because Babel cannot know whether
	// ignoring one would drop a terminal event.
	ErrUnknownEventType = errors.New("worker: unknown event type")

	// ErrOversizedLine reports a stdout line past Limits.MaxLineBytes.
	ErrOversizedLine = errors.New("worker: oversized event line")

	// ErrSequence reports a "seq" that is absent or not strictly increasing,
	// which means the stream was reordered, replayed or truncated.
	ErrSequence = errors.New("worker: event sequence violation")

	// ErrContainment reports a worker whose declared sandbox falls short of
	// the run's requirement, or that declared none. Babel does not implement
	// the sandbox (decision 53), so an undeclared or insufficient boundary is
	// refused before any job material reaches the worker rather than
	// discovered from its behaviour afterwards.
	ErrContainment = errors.New("worker: insufficient containment")

	// ErrEventOrder reports the resolved-configuration rule: exactly one
	// configuration event, first, before any other event.
	ErrEventOrder = errors.New("worker: event out of order")

	// ErrProfileMismatch reports a worker that resolved a different profile
	// than the job named. One profile applies to a run (SPEC.md §2.6), so a
	// substitution invalidates the receipt rather than merely surprising it.
	ErrProfileMismatch = errors.New("worker: resolved profile does not match the job")

	// ErrDuplicateResult reports a second result event.
	ErrDuplicateResult = errors.New("worker: duplicate result")

	// ErrEventAfterResult reports any event after a result.
	ErrEventAfterResult = errors.New("worker: event after result")

	// ErrResultAfterError reports a result after an error event. The error is
	// terminal: a worker that then claims success is contradicting itself.
	ErrResultAfterError = errors.New("worker: result after error")

	// ErrNoResult reports a worker whose stream ended, or whose process
	// exited, without a terminal event. A worker that only ever writes to
	// stderr and then exits lands here.
	ErrNoResult = errors.New("worker: exited without a result")

	// ErrWorkerStalled reports a worker that produced no event within
	// Limits.IdleTimeout. Stderr traffic deliberately does not reset the idle
	// timer, so a worker that chatters on stderr without making protocol
	// progress is stalled rather than busy.
	ErrWorkerStalled = errors.New("worker: stalled")

	// ErrWorkerLingered reports a worker that emitted a terminal event and
	// then failed to exit within Limits.ExitGrace. Babel kills the tree; the
	// run's output is still returned.
	ErrWorkerLingered = errors.New("worker: did not exit after its terminal event")

	// ErrDirtyExit reports a nonzero exit status after a successful result.
	// Babel owns the run's final status (SPEC.md §2.6), so a worker
	// contradicting its own result is a failure.
	ErrDirtyExit = errors.New("worker: nonzero exit after result")

	// ErrEventBudget reports more events than Limits.MaxEvents.
	ErrEventBudget = errors.New("worker: event budget exhausted")

	// ErrToolBudget reports more tool requests than Limits.MaxToolRequests
	// after every one of them has been denied. The denials keep the run
	// alive; a worker that ignores them indefinitely does not.
	ErrToolBudget = errors.New("worker: tool request budget exhausted")

	// ErrSecretDeclared reports metadata whose key names a credential.
	ErrSecretDeclared = errors.New("worker: declared credential-shaped metadata")

	// ErrWorkerReported matches any *WorkerError, for callers that only need
	// to know the counterpart failed.
	ErrWorkerReported = errors.New("worker: reported an error")
)

// decode parses one inbound line into v, and reports the field names v does
// not define. Unknown fields are preserved in the returned map and ignored by
// the caller: forward compatibility is a protocol requirement, not a
// tolerance.
func decode(line []byte, v any) (fields map[string]json.RawMessage, unknown []string, err error) {
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return nil, nil, err
	}
	known := knownFields(reflect.TypeOf(v).Elem())
	for name := range fields {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return fields, unknown, nil
}

// fieldCache memoizes each decoded type's JSON field names. Decoding happens
// once per line, so recomputing the reflected name set per line would be pure
// waste.
var fieldCache sync.Map // reflect.Type -> map[string]struct{}

func knownFields(t reflect.Type) map[string]struct{} {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.(map[string]struct{})
	}
	names := make(map[string]struct{}, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		name := field.Name
		if tag, ok := field.Tag.Lookup("json"); ok {
			if base, _, _ := strings.Cut(tag, ","); base != "" {
				name = base
			}
		}
		if name == "-" {
			continue
		}
		names[name] = struct{}{}
	}
	fieldCache.Store(t, names)
	return names
}

// Unsandboxed is the requirement for a run that needs no boundary, such as a
// configuration-only probe where nothing executes. It exists as a named value
// rather than a bare zero Requirement so that choosing it is visible in a call
// site and in review, instead of being the accident of an unset field.
func Unsandboxed() Requirement { return Requirement{} }
