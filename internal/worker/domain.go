// Package worker launches one analysis job inside Code's contained OMP engine
// and supervises it over OMP's native RPC (SPEC.md §2.6).
//
// Babel speaks nothing of its own on the pipe. Code launches `omp --mode rpc`
// inside its sandbox and forwards the engine's stdio byte for byte; Babel
// sends native commands — negotiate_protocol, set_host_tools, prompt,
// get_session_stats — and answers native host_tool_call frames. Everything
// Babel needs from the run travels as a host tool the model calls: the
// evidence facilities a job grants, and one tool that records the result
// under the job's own JSON Schema, which the engine validates before Babel
// ever sees a call. The one thing beside the stream is Code's runtime-info
// file, a private per-launch sidecar naming the profile, the privacy class,
// the cost estimate and the containment the engine runs under; Babel reads it
// after the engine's ready frame and refuses the launch — closing stdin before
// any prompt is written — when it falls short of what the run demands.
//
// What Babel owns is the boundary: which tools exist and what they answer,
// authorization of every call against the run's grant and policy, the
// lifetime of the whole process tree, the audit receipt, and the run's final
// status. What it never does is choose a model, retry, compact, or steer the
// model's turn — those are the engine's, and Babel's client is thin so that
// they stay there.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// ResultSchema is the schema every analysis result declares. It is the
// identifier of the payload shape internal/explore stores — Result and the
// frontier payloads it embeds — and the version suffix is semantic: a change
// to that shape is a new schema, because a payload interpreted under the wrong
// one would produce durable records nobody wrote.
const ResultSchema = "babel.analysis-result/1"

// RuntimeInfoSchema is the schema Code's runtime-info sidecar declares. The
// file is the one Code-specific document Babel reads; everything else on the
// boundary is OMP's own protocol.
const RuntimeInfoSchema = "code.runtime/1"

// Host tool names Babel registers with the engine. They are Babel's to define:
// the engine learns them from set_host_tools and the model reads them from the
// tool list, so nothing but this package and the facilities behind these names
// ever spells them.
//
// The submit tool is registered for every job. The others are registered only
// when the job grants the capability they serve, so a model never sees a tool
// it could not be allowed to call.
const (
	// ToolSubmit records the job's result. Its parameters are the job's
	// result schema, and the engine validates every call against it before
	// the call reaches Babel.
	ToolSubmit = "babel_submit_result"
	// ToolSearch is the corpus and frontier search facility
	// (CapabilityCorpusSearch).
	ToolSearch = "babel_corpus_search"
	// ToolSources and ToolFetch are the public-research broker's two
	// operations (CapabilityPublicResearch): the catalog of sources the
	// operator fixed, and a fetch of one of them by its opaque identifier.
	ToolSources = "babel_research_sources"
	ToolFetch   = "babel_research_fetch"
)

// capabilityTools is the single authority on which tool names serve which
// capability. Every other list of names is derived from it.
var capabilityTools = map[Capability][]string{
	CapabilityCorpusSearch:   {ToolSearch},
	CapabilityPublicResearch: {ToolSources, ToolFetch},
}

// ServesTool reports whether tool is one of the names Babel serves for c. A
// facility behind a capability calls it before answering, so a request naming
// an operation this build does not have is denied rather than guessed at.
func ServesTool(c Capability, tool string) bool {
	for _, name := range capabilityTools[c] {
		if name == tool {
			return true
		}
	}
	return false
}

// DenyUnservedTool is the denial a facility returns for a tool name it does not
// serve under c. It names what is served so the error is a remedy.
func DenyUnservedTool(c Capability, tool string) Decision {
	served := capabilityTools[c]
	if len(served) == 0 {
		return Decision{Reason: fmt.Sprintf("capability %s serves no tool in this build; %q cannot be answered", c, tool)}
	}
	return Decision{Reason: fmt.Sprintf("capability %s serves %s, not %q", c, strings.Join(served, ", "), tool)}
}

// Disclosure classes a grant can carry. The class is fixed before material is
// sent (SPEC.md §3), so it travels in the job rather than being negotiated.
const (
	DisclosureLocal  = "local"
	DisclosureHosted = "hosted"
)

// Capability names one evidence or execution facility a run may grant. The
// set matches the cookbook's `capabilities` front matter (SPEC.md §5.1) plus
// brokered public research (§2.6).
type Capability string

// Capabilities Babel defines. A tool naming anything else cannot be registered,
// because a policy cannot meaningfully reason about a capability Babel has no
// boundary for.
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

// String renders the reference for argv and diagnostics as id@revision, which
// is the form `code engine --profile` takes.
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

// HostTool is one tool Babel registers with the engine for a job: the native
// definition the model reads, and the capability the call is authorized
// under. The facility that serves the capability defines the parameters,
// because the argument shape belongs to what answers it; this package only
// checks that the capability is granted and that no two tools share a name.
type HostTool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema of the call's arguments. The engine
	// validates every call against it before Babel is asked, so a facility
	// may decode arguments strictly and treat a mismatch as its own bug.
	Parameters json.RawMessage
	Capability Capability
	// LoadMode is the engine's "essential" or "discoverable"; empty is the
	// engine's default.
	LoadMode string
}

// OutputContract is the shape a job's result must take: the schema
// identifier a receipt records, the JSON Schema the submit tool is registered
// with, and the instructions the prompt opens with. It is Babel's own
// document, generated by the facility that stores the result, and the engine
// enforces the schema structurally on every submission.
type OutputContract struct {
	Schema       string
	JSONSchema   json.RawMessage
	Instructions string
}

// Job is one analysis job: what the engine is asked, what it may call, and
// what it must produce.
type Job struct {
	// JobID and RunID identify the run in the receipt.
	JobID string
	RunID string

	// Profile is the Code profile the engine must run under. Babel passes it
	// on argv and refuses a launch whose runtime-info names another.
	Profile ProfileRef

	// Recipes, Grant and Sources are the run's boundary and its content,
	// recorded in the receipt. Sources travel to the model only through the
	// prompt the caller wrote and through the evidence tools it granted.
	Recipes []RecipeRef
	Grant   Grant
	Sources []Source

	// Params are the caller's own key/value facts about the run, rendered
	// into the prompt by the caller and recorded here for the receipt.
	Params map[string]string

	// Tools are the evidence facilities the model may call. Each must name a
	// granted capability; Run refuses the job before launch otherwise.
	Tools []HostTool

	// Output is the result contract. Its schema is registered as ToolSubmit's
	// parameters.
	Output OutputContract

	// Prompt is the whole of what the model is told, composed by the caller
	// from the output instructions, the recipes, the approved sources and
	// whatever prior records it examines. It is written only after the
	// engine's runtime-info has satisfied the run's containment requirement.
	Prompt string

	// Accept validates one submission's payload as the caller's own domain
	// shape and returns the reason it is refused. The engine has already
	// enforced the JSON Schema; this is the semantic check — references,
	// authority, provenance. A refused submission is answered to the model
	// as a tool error so it can correct itself, and never replaces an
	// earlier accepted one. Nil accepts every schema-valid payload.
	Accept func(payload json.RawMessage) error
}

// validate refuses a job Run could not honestly launch.
func (j Job) validate() error {
	if j.Profile.ID == "" {
		return errors.New("worker: job names no profile")
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return errors.New("worker: job carries no prompt")
	}
	if len(j.Output.JSONSchema) == 0 {
		return errors.New("worker: job carries no result schema")
	}
	if !json.Valid(j.Output.JSONSchema) {
		return errors.New("worker: the result schema is not JSON")
	}
	seen := map[string]bool{ToolSubmit: true}
	for _, tool := range j.Tools {
		switch {
		case strings.TrimSpace(tool.Name) == "":
			return errors.New("worker: a host tool has no name")
		case seen[tool.Name]:
			return fmt.Errorf("worker: host tool %q is registered twice", tool.Name)
		case !tool.Capability.Known():
			return fmt.Errorf("worker: host tool %q serves capability %q, which Babel does not define", tool.Name, tool.Capability)
		case !j.Grant.Allows(tool.Capability):
			return fmt.Errorf("worker: host tool %q serves %s, which this job does not grant", tool.Name, tool.Capability)
		case !ServesTool(tool.Capability, tool.Name):
			return fmt.Errorf("worker: host tool %q is not a name Babel serves for %s", tool.Name, tool.Capability)
		case len(tool.Parameters) == 0 || !json.Valid(tool.Parameters):
			return fmt.Errorf("worker: host tool %q has no JSON Schema", tool.Name)
		case strings.TrimSpace(tool.Description) == "":
			// The engine refuses a tool with no description at
			// registration; refusing here keeps that from costing a launch.
			return fmt.Errorf("worker: host tool %q has no description", tool.Name)
		}
		seen[tool.Name] = true
	}
	return nil
}

// Identity names the worker build that produced a receipt.
type Identity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DenyCode explains a denial in a form both the model and a later reviewer
// can act on. They are ordered by the check that produces them, and the
// order is part of the contract: unknown-tool precedes limit precedes policy.
type DenyCode string

// Denial reasons.
const (
	// DenyUnknownTool names a call to a tool the job did not register. The
	// engine only calls registered tools, so this is a defect on the far
	// side; it is refused rather than inferred into a capability.
	DenyUnknownTool DenyCode = "unknown-tool"
	// DenyLimit is a call past the run's tool budget.
	DenyLimit DenyCode = "limit"
	// DenyPolicy is the injected Authorizer's own refusal.
	DenyPolicy DenyCode = "policy"
)

// Resources is the engine's measured resource use, read from Code's finished
// runtime-info report. Every figure is a pointer: a launch Code could not
// measure reports nothing, which is a different claim from zero, and the
// receipt keeps that difference.
type Resources struct {
	CPUSeconds          *float64 `json:"cpu_seconds,omitempty"`
	MaxRSSBytes         *int64   `json:"max_rss_bytes,omitempty"`
	SandboxBytesWritten *int64   `json:"sandbox_bytes_written,omitempty"`
	// Provenance is Code's own statement of which reading each figure came
	// from, kept verbatim so a reviewer can weigh the measurement.
	Provenance string `json:"provenance,omitempty"`
}

// Usage is what the engine's own session accounting reports at the end of a
// run (get_session_stats). It is the engine's measurement of the model work
// it did, not a cost guard: Cost is in the profile's own units.
type Usage struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Cost             float64 `json:"cost"`
	ToolCalls        int     `json:"tool_calls"`
	Messages         int     `json:"messages"`
}

// NativeUsage preserves the per-message Usage vocabulary from OMP v18.1.12
// packages/catalog/src/types.ts. Nil fields mean unavailable, never zero.
// ReasoningTokens is a subset of Output; these observations are not added to
// the independently reported session Usage totals.
type NativeUsage struct {
	Input           *float64 `json:"input,omitempty"`
	Output          *float64 `json:"output,omitempty"`
	CacheRead       *float64 `json:"cacheRead,omitempty"`
	CacheWrite      *float64 `json:"cacheWrite,omitempty"`
	TotalTokens     *float64 `json:"totalTokens,omitempty"`
	ContextTokens   *float64 `json:"contextTokens,omitempty"`
	PremiumRequests *float64 `json:"premiumRequests,omitempty"`
	ReasoningTokens *float64 `json:"reasoningTokens,omitempty"`
	Orchestration   *struct {
		Input     *float64 `json:"input,omitempty"`
		CacheRead *float64 `json:"cacheRead,omitempty"`
		Output    *float64 `json:"output,omitempty"`
	} `json:"orchestration,omitempty"`
	CTTL *struct {
		Ephemeral5m *float64 `json:"ephemeral5m,omitempty"`
		Ephemeral1h *float64 `json:"ephemeral1h,omitempty"`
	} `json:"cttl,omitempty"`
	Server *struct {
		WebSearch *float64 `json:"webSearch,omitempty"`
		WebFetch  *float64 `json:"webFetch,omitempty"`
	} `json:"server,omitempty"`
	Credits *struct {
		Cost          *float64 `json:"cost,omitempty"`
		CommittedCost *float64 `json:"committedCost,omitempty"`
		ACUCost       *float64 `json:"acuCost,omitempty"`
	} `json:"credits,omitempty"`
	Cost *struct {
		Input      *float64 `json:"input,omitempty"`
		Output     *float64 `json:"output,omitempty"`
		CacheRead  *float64 `json:"cacheRead,omitempty"`
		CacheWrite *float64 `json:"cacheWrite,omitempty"`
		Total      *float64 `json:"total,omitempty"`
	} `json:"cost,omitempty"`
}

// AssistantMessageAccounting is the content-free projection of one native
// assistant message_end. Seq orders it with FallbackRecord in the received
// event stream; At is Babel's observation time. Timestamp and CompletedAt are
// native milliseconds since epoch. Missing native fields remain unavailable;
// declared profile metadata never supplies them.
type AssistantMessageAccounting struct {
	Seq              int          `json:"seq"`
	At               time.Time    `json:"at"`
	Provider         string       `json:"provider,omitempty"`
	Model            string       `json:"model,omitempty"`
	UpstreamProvider string       `json:"upstreamProvider,omitempty"`
	UpstreamModel    string       `json:"upstreamModel,omitempty"`
	Usage            *NativeUsage `json:"usage,omitempty"`
	StopReason       string       `json:"stopReason,omitempty"`
	Timestamp        *int64       `json:"timestamp,omitempty"`
	CompletedAt      *int64       `json:"completedAt,omitempty"`
	ResponseID       string       `json:"responseId,omitempty"`
}

// FallbackRecord preserves one native retry_fallback_applied or
// retry_fallback_succeeded event, not an inferred pairing. Applied carries
// From/To/Role; succeeded carries Model/Role. An applied edge without a later
// succeeded event has an unknown outcome, not an implied success or failure.
type FallbackRecord struct {
	Seq   int       `json:"seq"`
	At    time.Time `json:"at"`
	Type  string    `json:"type"`
	From  string    `json:"from,omitempty"`
	To    string    `json:"to,omitempty"`
	Model string    `json:"model,omitempty"`
	Role  string    `json:"role,omitempty"`
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

// Containment is the sandbox Code declares it launched the engine into. Babel
// does not implement the sandbox — Code owns it, because Code owns the
// profile, the provider credential and the engine process (SPEC §2.6,
// decision 53) — so Babel's containment is only as good as this declaration
// plus what the conformance suite checks. That is exactly why the declaration
// is mandatory rather than advisory: prompting an engine in an unspecified
// sandbox would be trusting a boundary nobody stated.
//
// Every field is a claim by Code about the launch. Babel cannot verify a claim
// from outside the process, and does not pretend to: it refuses a launch whose
// declaration falls short of the run's requirement before any prompt is
// written, and records the declaration in the receipt so a later reviewer sees
// which boundary the evidence was produced behind.
type Containment struct {
	// Backend names the mechanism, for the receipt and for an operator
	// deciding whether to trust it. Free-form because the set is Code's to
	// grow, but empty is refused: an unnamed mechanism cannot be assessed.
	Backend string `json:"backend"`
	// FilesystemIsolation reports that the engine's writes cannot reach the
	// host filesystem outside what Code mounted for it.
	FilesystemIsolation bool `json:"filesystem_isolation"`
	// NetworkDefaultDeny reports that egress is denied except to the
	// provider. Public research reaches the network through Babel's broker
	// over the pipe, never from inside the sandbox.
	NetworkDefaultDeny bool `json:"network_default_deny"`
	// ResourceCeilings reports that CPU, memory and disk are bounded, so a
	// run cannot exhaust the machine that hosts the archive.
	ResourceCeilings bool `json:"resource_ceilings"`
	// Disposable reports that the execution environment is destroyed at
	// teardown, so nothing a run wrote survives into the next one.
	Disposable bool `json:"disposable"`
	// Escape is Code's own statement of what the sandbox does not contain.
	// It is required and may not be empty: a sandbox whose author claims no
	// residual risk has not been thought about, and §10 requires
	// uncertainty to stay visible rather than be rounded to zero.
	Escape string `json:"escape"`
}

// Requirement is the containment a run demands. Babel refuses a launch that
// declares less before the prompt — the recipes, the sources, the brief —
// reaches the engine: what a refused engine has seen is the profile it was
// launched under and the tool names Babel would have registered, and nothing
// about what it would have read.
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

// Unsandboxed is the requirement of a run that genuinely needs no boundary.
// It exists so that relaxing the default is a statement in the caller's code
// rather than a zero value.
func Unsandboxed() Requirement { return Requirement{} }

// Satisfies reports whether a declaration meets a requirement on the platform
// Babel is running on, naming every shortfall rather than the first, so an
// operator sees the whole gap in one message instead of fixing them one launch
// at a time.
func (c Containment) Satisfies(r Requirement) error {
	return c.satisfiesOn(r, runtime.GOOS)
}

// demandsContainment reports whether r asks for any boundary at all. A run that
// asks for none — a configuration-only probe, where nothing executes, or one
// the operator relaxed per run — has no boundary to disbelieve, so the platform
// gate below does not apply to it.
func (r Requirement) demandsContainment() bool {
	return r.FilesystemIsolation || r.NetworkDefaultDeny || r.ResourceCeilings || r.Disposable
}

// satisfiesOn is Satisfies against an explicit host platform, which is what
// makes the §10 gate exercisable from both sides of it rather than only on
// whichever machine the test happens to run on.
//
// The platform is checked before the properties, and checked as a refusal
// rather than as a phrasing choice: a platform with no backend that has passed
// its escape scenario must not run analysis whatever Code claims, since the
// claim is exactly what §10 declines to take on faith. It is scoped to runs
// that demand containment, because a run that demands none is not relying on a
// boundary in the first place.
func (c Containment) satisfiesOn(r Requirement, goos string) error {
	if strings.TrimSpace(c.Backend) == "" {
		return fmt.Errorf("%w: Code declared no sandbox backend", ErrContainment)
	}
	if strings.TrimSpace(c.Escape) == "" {
		return fmt.Errorf("%w: Code declared no escape assumption for backend %q", ErrContainment, c.Backend)
	}
	if r.demandsContainment() && !platformQualified(goos) {
		return &platformRefusal{goos: goos, backend: c.Backend}
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

// Configuration is a resolved Code profile as `code engine --describe` reports
// it: the reference Babel persists plus the non-secret metadata a receipt and
// a consent prompt need. Nothing executes to produce it.
type Configuration struct {
	Profile  ProfileRef
	Privacy  Privacy
	Cost     Cost
	Metadata map[string]string
	Worker   Identity
	// Unknown lists top-level fields this build did not read, so a newer
	// Code's additions are visible rather than silently dropped.
	Unknown []string
}

// secretKeyMarkers are metadata key fragments that name a credential. Metadata
// is recorded in receipts and displayed to operators, so a profile that
// declares a secret under any of these is refused whole (ErrSecretDeclared)
// rather than having one value redacted.
var secretKeyMarkers = []string{"key", "token", "secret", "password", "credential", "auth"}

// validateMetadata refuses metadata whose keys name a credential.
func validateMetadata(metadata map[string]string) error {
	for key := range metadata {
		lower := strings.ToLower(key)
		for _, marker := range secretKeyMarkers {
			if strings.Contains(lower, marker) {
				return fmt.Errorf("%w: metadata key %q", ErrSecretDeclared, key)
			}
		}
	}
	return nil
}

// Errors a supervised run can produce. They are sentinels because a caller
// decides differently between an engine that never spoke, one Babel refused,
// one that stalled, and one that answered and then would not leave.
var (
	// ErrHandshakeTimeout reports no ready frame within Limits.HandshakeTimeout.
	ErrHandshakeTimeout = errors.New("worker: engine did not become ready in time")
	// ErrProtocolMismatch reports a first frame that is not the engine's
	// ready frame, or a ready frame that does not offer RPC protocol v2.
	ErrProtocolMismatch = errors.New("worker: engine does not speak the expected protocol")
	// ErrRuntimeInfo reports a launch whose runtime-info sidecar is missing
	// or unreadable once the engine is ready. Code writes it before
	// forwarding a byte, so its absence means the launch is not Code's.
	ErrRuntimeInfo = errors.New("worker: runtime-info is missing or invalid")
	// ErrProfileMismatch reports a launch running under a profile other
	// than the one the job named.
	ErrProfileMismatch = errors.New("worker: engine resolved a different profile than the job named")
	// ErrContainment reports a declared sandbox that falls short of the run's
	// requirement. The prompt is never written.
	ErrContainment = errors.New("worker: engine containment does not satisfy the run")
	// ErrPlatformUnqualified reports a host platform with no qualified
	// sandbox backend (SPEC.md §10).
	ErrPlatformUnqualified = errors.New("worker: no sandbox backend is qualified on this platform")
	// ErrSecretDeclared reports profile metadata naming a credential.
	ErrSecretDeclared = errors.New("worker: profile metadata declares a credential")
	// ErrMalformedFrame reports a stdout line, or a reassembled chunk
	// sequence, that is not one JSON object.
	ErrMalformedFrame = errors.New("worker: engine wrote a frame Babel cannot decode")
	// ErrOversizedFrame reports a physical line over Limits.MaxFrameBytes or
	// a chunk sequence over Limits.MaxReassembledBytes.
	ErrOversizedFrame = errors.New("worker: engine frame exceeds the transport bound")
	// ErrCommandFailed reports an engine response of success:false to a
	// command Babel needs answered.
	ErrCommandFailed = errors.New("worker: engine refused a command")
	// ErrNoResult reports a run that ended without an accepted submission.
	ErrNoResult = errors.New("worker: engine ended without submitting a result")
	// ErrEngineExited reports an engine that left before its turn ended:
	// stdout closed, or the process was reaped, with no agent_end. The
	// exit status in the message says whether it crashed or Code refused
	// the launch. It is distinct from ErrNoResult, which is a turn that
	// ended with nothing accepted.
	ErrEngineExited = errors.New("worker: engine exited before its turn ended")
	// ErrWorkerStalled reports no frame within Limits.IdleTimeout.
	ErrWorkerStalled = errors.New("worker: engine went silent")
	// ErrWorkerLingered reports a process still alive after its stdin closed
	// and Limits.ExitGrace elapsed.
	ErrWorkerLingered = errors.New("worker: engine did not exit after the run")
	// ErrDirtyExit reports a non-zero exit status after an otherwise
	// complete run.
	ErrDirtyExit = errors.New("worker: engine exited with a failure status")
	// ErrEventBudget reports a stream past Limits.MaxEvents.
	ErrEventBudget = errors.New("worker: engine exceeded the event budget")
	// ErrToolBudget reports an engine that kept calling tools after the
	// budget was exhausted and every further call denied.
	ErrToolBudget = errors.New("worker: engine exceeded the tool budget")
)
