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
// internal/restic follows for the repository password. It also travels in two
// stages rather than one, and the credential is in the second: see "Worker
// mode" below for what that buys and what it cost to get.
//
// # Handshake
//
// The worker speaks first, before reading anything, so Babel can refuse an
// incompatible counterpart before any job material is written to it:
//
//	{"type":"hello","protocol":"babel.analysis-worker","versions":[2],
//	 "modes":["configure","worker"],
//	 "worker":{"name":"code","version":"1.2.3"}}
//
// Babel replies with exactly one of:
//
//	{"type":"accept","protocol":"babel.analysis-worker","version":2,
//	 "mode":"worker",
//	 "limits":{"max_line_bytes":1048576,"max_events":100000,
//	           "max_tool_requests":1024,"idle_seconds":120,
//	           "exit_grace_seconds":5}}
//
//	{"type":"refuse","protocol":"babel.analysis-worker",
//	 "reason":"no mutually supported protocol version","supported":[2]}
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
// The job document is staged, and the staging is the boundary. Babel writes
// only what the worker needs to resolve and declare itself, holds the run's
// material and its credential back until that declaration has been accepted,
// and writes the rest afterwards. A worker whose sandbox falls short of the
// run's requirement is therefore refused having seen a profile reference and
// nothing else: not the corpus selection, not the capability grant, not the
// run-scoped broker token.
//
// Stage one is written immediately after accept:
//
//	{"type":"job-preamble","protocol":"babel.analysis-worker",
//	 "job_id":"j-1","run_id":"r-1",
//	 "profile":{"id":"p-1","revision":4},
//	 "params":{"…":"…"}}
//
// The parameters travel here because they are how Babel tells a worker which
// kind of run this is — the stage it is running, the conformance obligation
// being exercised — and a worker resolves a profile differently for a
// conformance probe than for an analysis. They carry no credential and no
// archive content; the material is in stage two.
//
// The worker answers with the resolved configuration described below, which
// carries its containment declaration, and Babel writes nothing further until
// it arrives. An accepted declaration is answered with stage two:
//
//	{"type":"job","protocol":"babel.analysis-worker",
//	 "job_id":"j-1","run_id":"r-1",
//	 "recipes":[{"id":"outcome-integrity","version":1}],
//	 "grant":{"capabilities":["corpus-search"],"disclosure":"local",
//	          "tools":{"corpus-search":["search"]},
//	          "expires":"2026-08-29T12:00:00Z"},
//	 "sources":[{"kind":"session","selector":"omp/…","digest":"sha256:…",
//	             "snapshot":"…"}],
//	 "broker":{"endpoint":"http://127.0.0.1:0","token":"…"}}
//
// Both stages carry the same job_id and run_id. Each line is self-identifying
// for it, so a worker can refuse a pairing that does not belong to one run
// instead of analysing one run's material under another's identity.
//
// A declaration Babel does not accept is answered with the handshake's own
// refusal message instead, and for the handshake's reason: the worker is
// blocked on a read, and killing it without a word would leave it unable to
// tell a boundary it must fix from a supervisor that crashed.
//
//	{"type":"refuse","protocol":"babel.analysis-worker",
//	 "reason":"declared containment falls short of this run's requirement"}
//
// This is what version 2 is. Version 1 wrote the whole document — sources,
// grant and broker token — immediately after the handshake and checked the
// declaration when the first event arrived, so a worker that under-declared
// executed no analysis but already held the run's credential and knew the
// selected corpus. The two shapes are not compatible in either direction: a
// version 1 worker waits for a document this Babel no longer sends, and a
// version 2 worker waits for material a version 1 Babel has already sent. An
// empty version intersection is refused, naming the version Babel requires,
// rather than bridged by a shim that would keep the old ordering reachable.
//
// The grant's "tools" object is the operation vocabulary Babel will answer:
// for each granted capability, the tool names some facility in this build
// actually serves. It is published rather than assumed because the worker is
// a separate program in a separate repository, and a name it invents for
// itself is denied on every request while looking, from the worker's side,
// exactly like a run with nothing to find. That is not a hypothesis: the
// first real exploration Babel ever drove asked for "babel_corpus_search",
// was denied three times out of three, and produced no evidence at all.
//
// A granted capability that no facility in this build brokers has no key —
// never a key with an empty array. The two would say the same thing in two
// shapes, and an empty array is the ambiguous one: it reads equally as "this
// facility exposes no operations" and as "someone forgot to publish them".
// Absence states the only true thing, that nothing behind the capability
// answers, and a worker must request nothing under it rather than fall back
// to a name of its own. The whole object is omitted when no granted
// capability is served, which is a different fact from a Babel that predates
// the field and never published one.
//
// The worker then streams events on stdout. Every event carries "seq",
// strictly increasing from 1 across all event types, so a dropped or reordered
// stream is detectable rather than merely wrong. The first event must be a
// "configuration" reporting the profile the worker actually resolved — Babel
// requires it because §6.5 makes resolved provider metadata part of the
// receipt, and it must name the profile the preamble named
// (ErrProfileMismatch). It is also the answer to the preamble, so it is the
// one event Babel waits for synchronously.
//
// In worker mode that configuration must also declare the sandbox the worker
// provides. Babel does not implement one — Code owns the disposable sandbox and
// credential isolation (SPEC.md §2.6, decision 53) — so the declaration is what
// Babel holds the worker to, and it is what stage two is conditional on: a
// declaration short of the run's requirement is refused (ErrContainment) with
// the credential still unwritten.
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
// An "error" event is the one other thing a worker may send before it has
// declared anything: a worker that cannot resolve the profile the preamble
// named has no configuration to report and says so. That ends the run, and it
// ends it with stage two unwritten. Anything else before the declaration —
// progress, a tool request, a result — is an ordering violation
// (ErrEventOrder), and it is refused the same way an insufficient declaration
// is, because a worker producing output before it has stated its boundary is
// exactly the worker the staging exists to stop.
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
//	{"type":"tool-decision","request_id":"t-1","decision":"allow",
//	 "reason":"served 3 hits from the corpus index",
//	 "results":{"schema":"babel.corpus-search/1","query":"…","limit":10,
//	            "hits":[{"harness":"omp","source_id":"…","index":42,
//	                     "kind":"tool-observation","excerpt":"…",
//	                     "locator":{"path":"…","line":12,"byte_offset":3456,
//	                                "digest":"…"}}]}}
//	{"type":"tool-decision","request_id":"t-1","decision":"deny",
//	 "code":"not-granted","reason":"…"}
//
// "results" is the evidence the facility served, in that facility's own shape,
// and it is optional. Absent means no payload travelled at all: that is what a
// denial sends, what a capability with no serving facility sends, and what a
// Babel predating the field sends. A facility that searched and found nothing
// sends its own empty answer — for corpus search, "hits":[] — because a worker
// that was served nothing and a corpus that holds nothing are different facts
// and are reported as different gaps.
//
// Every hit a corpus search serves carries the harness, the source identity,
// the locator that recovers the record's bytes from the archive, and a bounded
// redacted excerpt. The excerpt is what lets a model form an observation; the
// locator is what lets a human reopen that observation against the archive,
// which §9 requires of every claim. The payload travels on the pipe and never
// into a receipt.
//
// A denial is not a termination. The worker adapts and keeps working, and it
// must still deliver a terminal event.
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
// The tool name inside a granted capability is held to the same published
// mapping the job carried, and that check sits in the Authorizer rather than
// in the list above because which operations exist is a fact about the
// facility behind the capability, not about the grant. It is not, however, a
// check an Authorizer is free to skip: ServesTool and DenyUnservedTool are
// the one predicate and the one denial every authorizer in this module uses,
// so the permissive policy the conformance suite grades with and the
// production policy internal/explore installs refuse an unpublished name
// identically. They have to. A suite whose policy is more permissive than
// production's certifies a worker that cannot work, which is precisely how a
// worker asking for "babel_corpus_search" reached 14/14 and then retrieved
// nothing.
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
	"runtime"
	"slices"
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
//
// Version 2 staged the job document: the preamble, the worker's containment
// declaration, and only then the material and the run's broker credential.
// Version 1 wrote all of it at once. The ordering is the whole of what the
// declaration buys, so the two are not interchangeable and no shim bridges
// them — an empty version intersection is refused, naming this version.
const ProtocolVersion = 2

// Message types on the wire. Babel writes accept, refuse, job-preamble, job
// and tool-decision; the worker writes hello, configuration, progress,
// tool-request, result and error.
const (
	MessageHello         = "hello"
	MessageAccept        = "accept"
	MessageRefuse        = "refuse"
	MessageJobPreamble   = "job-preamble"
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

// ResultSchema is the schema every analysis result must declare, and it lives
// here because it is wire surface: it is written by a worker developed in a
// separate repository and read by Babel, so the string belongs with the rest
// of the protocol's vocabulary rather than with either side's interpretation
// of it. Defining it in a consumer package is what previously let a second,
// divergent copy appear in a worker implementation, which is a drift no test
// on either side could see.
//
// The version suffix is semantic: any change to the payload shape Babel
// stores under it — internal/explore's Result and the internal/frontier
// payloads it embeds — is a new schema, because a payload interpreted under
// the wrong one would produce durable records nobody wrote.
const ResultSchema = "babel.analysis-result/1"

// ToolSearch is the tool name Babel serves for CapabilityCorpusSearch, and it
// lives beside ResultSchema for the identical reason: it is a string one
// repository writes and the other reads, so a copy held by either consumer is
// a drift neither side's tests can see. ResultSchema's comment above calls
// that "a drift no test on either side could see" — it happened again, one
// concept over, and this is where the concept now lives so it cannot happen a
// third time.
//
// The capability says which facility; the tool says which operation inside it,
// and an unrecognized operation is denied rather than guessed at.
const ToolSearch = "search"

// capabilityTools is the single authority on which tool names Babel serves for
// each capability, and every consumer reads it rather than restating it: the
// job document publishes it to the worker (Job.encodeMaterial), the facility
// behind corpus-search enforces it (internal/explore's authorizer), and the
// conformance suite grades a candidate against it (AllowWithinGrant and the
// run/published-tool-names obligation). Adding a name here is what makes it
// exist for all three at once, and there is nowhere else it can be added.
//
// A capability no facility in this build brokers is absent from the map rather
// than mapped to an empty list. See the "tools" paragraph in this package's
// documentation for why absence is the representation; the short form is that
// an empty list cannot distinguish "serves no operations" from "was never
// published", and only one of those is ever true. repo-read, sandbox-exec and
// public-research are all in that position today, which internal/explore
// denies in those words.
var capabilityTools = map[Capability][]string{
	CapabilityCorpusSearch: {ToolSearch},
}

// ServesTool reports whether tool is a name Babel published for c. It is
// exported because it is the whole of the name discipline: an Authorizer that
// wants to be as strict as production is strict by calling this, and the
// conformance suite's permissive policy calls the same function the production
// authorizer does.
func ServesTool(c Capability, tool string) bool {
	return slices.Contains(capabilityTools[c], tool)
}

// DenyUnservedTool refuses a tool name c does not serve, and it lives here so
// that the denial a worker reads is one sentence written once. The two cases
// are different facts and read differently: a facility that serves operations
// but not this one, and a capability nothing in this build serves at all.
func DenyUnservedTool(c Capability, tool string) Decision {
	if len(capabilityTools[c]) == 0 {
		return Decision{Reason: fmt.Sprintf("%s is granted but no facility in this build serves it", c)}
	}
	return Decision{Reason: fmt.Sprintf("%s has no tool %q", c, tool)}
}

// publishedTools is the mapping one job publishes: every granted capability
// some facility in this build serves, and no key at all for one that none
// does. A grant that reaches nothing served publishes nothing, which is not
// the same wire fact as an older Babel that never had the field.
func publishedTools(g Grant) map[Capability][]string {
	var published map[Capability][]string
	for _, c := range g.Capabilities {
		served := capabilityTools[c]
		if len(served) == 0 {
			continue
		}
		if published == nil {
			published = make(map[Capability][]string, len(g.Capabilities))
		}
		published[c] = served
	}
	return published
}

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

// Job is one analysis job. It is one Go value and two wire messages: the
// preamble Babel writes before the worker has declared anything, and the
// material it writes only once that declaration has been accepted. Which
// field travels in which stage is the boundary this type exists to draw, so
// it is stated per field below rather than left to the encoders.
//
// PreambleExtra and Extra carry forward-compatible top-level fields a newer
// Babel may add, merged into their own stage's document. They are two maps
// because the stages are two promises: a field a newer Babel adds to the
// material must not be smuggled into the preamble, which is the message Babel
// promises carries nothing the worker has not yet earned.
type Job struct {
	// JobID and RunID identify the run. They travel in both stages, so
	// either line is self-identifying and a worker can refuse a pairing
	// that does not belong to one run.
	JobID string
	RunID string

	// Profile and Params travel in the preamble: they are what a worker
	// needs to resolve itself and to know which kind of run this is.
	Profile ProfileRef
	Params  map[string]string

	// Recipes, Grant, Sources and Broker travel in the material stage. They
	// are the run's boundary and its content — what may be asked for, what
	// may be read, and the credential that opens the evidence API — and a
	// worker that has not declared an acceptable sandbox never sees them.
	Recipes []RecipeRef
	Grant   Grant
	Sources []Source
	Broker  Broker

	PreambleExtra map[string]json.RawMessage
	Extra         map[string]json.RawMessage
}

// secrets lists the values in j that must never appear in a receipt, an error
// or a diagnostic line.
func (j Job) secrets() []string {
	if j.Broker.Token == "" {
		return nil
	}
	return []string{j.Broker.Token}
}

// preambleWire is the encoded form of stage one: the run's identity, the
// profile to resolve, and the parameters that say what kind of run this is.
// Nothing else, and the omission is the feature.
type preambleWire struct {
	Type     string            `json:"type"`
	JobID    string            `json:"job_id"`
	RunID    string            `json:"run_id"`
	Profile  ProfileRef        `json:"profile"`
	Params   map[string]string `json:"params,omitempty"`
	Protocol string            `json:"protocol"`
}

// jobWire is the encoded form of stage two. It exists separately from Job so
// the exported type can hold Go values (time.Time, Capability) while the wire
// stays the documented JSON shape.
type jobWire struct {
	Type     string      `json:"type"`
	JobID    string      `json:"job_id"`
	RunID    string      `json:"run_id"`
	Recipes  []RecipeRef `json:"recipes,omitempty"`
	Grant    grantWire   `json:"grant"`
	Sources  []Source    `json:"sources,omitempty"`
	Broker   *brokerWire `json:"broker,omitempty"`
	Protocol string      `json:"protocol"`
}

// grantWire carries the boundary and the operation vocabulary inside it. Tools
// is derived at encode time from capabilityTools rather than being a field of
// the exported Grant: nothing may hand Babel a mapping, because a caller-set
// mapping is a second list of names, and a second list is the defect.
type grantWire struct {
	Capabilities []Capability            `json:"capabilities"`
	Disclosure   string                  `json:"disclosure"`
	Tools        map[Capability][]string `json:"tools,omitempty"`
	Expires      *time.Time              `json:"expires,omitempty"`
}

type brokerWire struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

// encodePreamble encodes stage one. It is a method rather than a MarshalJSON
// so that no caller can encode "a job" by accident: a Job has two encodings
// and one of them must not travel yet, so the choice is made by name at every
// call site.
func (j Job) encodePreamble() ([]byte, error) {
	return mergeExtra(preambleWire{
		Type:     MessageJobPreamble,
		Protocol: ProtocolName,
		JobID:    j.JobID,
		RunID:    j.RunID,
		Profile:  j.Profile,
		Params:   j.Params,
	}, j.PreambleExtra, MessageJobPreamble)
}

// encodeMaterial encodes stage two: the recipes, the grant, the sources and
// the broker credential. It is only ever called after a containment
// declaration has satisfied the run's requirement.
func (j Job) encodeMaterial() ([]byte, error) {
	w := jobWire{
		Type:     MessageJob,
		Protocol: ProtocolName,
		JobID:    j.JobID,
		RunID:    j.RunID,
		Recipes:  j.Recipes,
		Grant: grantWire{
			Capabilities: j.Grant.Capabilities,
			Disclosure:   j.Grant.Disclosure,
			Tools:        publishedTools(j.Grant),
		},
		Sources: j.Sources,
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
	return mergeExtra(w, j.Extra, MessageJob)
}

// mergeExtra encodes one stage and merges its forward-compatible fields at the
// top level. A collision with a documented field is an error rather than a
// silent overwrite: a caller redefining "grant" through Extra would be quietly
// rewriting the capability boundary, and one redefining "profile" through
// PreambleExtra would be quietly changing what the worker declares against.
func mergeExtra(stage any, extra map[string]json.RawMessage, name string) ([]byte, error) {
	encoded, err := json.Marshal(stage)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for field, value := range extra {
		if _, taken := merged[field]; taken {
			return nil, fmt.Errorf("job: extra field %q collides with a documented field of the %s message", field, name)
		}
		merged[field] = value
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
// for material that will never arrive. Two moments write it: the handshake,
// and a containment declaration that did not satisfy the run.
//
// Supported is omitted rather than empty for the second of those. The version
// list is the remedy for a version refusal and nothing else, and a worker told
// "supported":[2] while being refused for its sandbox would be sent to fix the
// one thing that was not wrong.
type refuseMessage struct {
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	Reason    string `json:"reason"`
	Supported []int  `json:"supported,omitempty"`
}

// decisionMessage is Babel's answer to one tool-request.
//
// Results is the evidence the facility behind the capability served, as the
// facility's own JSON. It is optional and absent means one thing only: no
// payload travelled. That is what every denial sends, what every capability
// with no serving facility sends, and what a Babel predating the field sends.
// A facility that searched and matched nothing sends its own empty answer
// instead, because "Babel served me nothing" and "the corpus holds nothing"
// are different facts and a worker reports them as different gaps.
//
// It exists because a decision without it is unusable as evidence. Before this
// field the whole of what a worker learned from an allowed corpus search was
// the sentence "served N hits from the corpus index": Babel computed the hits,
// redacted them, recorded their locators, and discarded them. The first real
// exploration retrieved four times, was allowed four times, and wrote nothing,
// because it had never been shown a byte of the corpus.
//
// The field is written to the pipe and nowhere else. Babel's receipt records
// the decision, the argument digest and the retrieval trace's locators, and
// never this payload: §9 forbids the durable record becoming a plaintext store
// of archive content readable by anyone with catalog access.
type decisionMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Decision  string          `json:"decision"`
	Code      DenyCode        `json:"code,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Results   json.RawMessage `json:"results,omitempty"`
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

// Requirement is the containment a run demands. Babel refuses a worker that
// declares less before the run's recipes, grant, sources or broker credential
// reach it: the declaration answers a preamble that carries a profile
// reference and the run's parameters, so what a refused worker has seen is
// which profile it was asked to be and nothing about what it would have read.
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
// its escape scenario must not run analysis whatever a worker claims, since the
// claim is exactly what §10 declines to take on faith. It is scoped to runs
// that demand containment, because a run that demands none is not relying on a
// boundary in the first place.
func (c Containment) satisfiesOn(r Requirement, goos string) error {
	if strings.TrimSpace(c.Backend) == "" {
		return fmt.Errorf("%w: worker declared no sandbox backend", ErrContainment)
	}
	if strings.TrimSpace(c.Escape) == "" {
		return fmt.Errorf("%w: worker declared no escape assumption for backend %q", ErrContainment, c.Backend)
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
	// before any job material is written, and the refusal names the versions
	// Babel supports so the counterpart learns which one to speak.
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
	// refused with the run's material and its credential still unwritten: the
	// declaration answers the preamble, and stage two is what accepting it
	// buys. The worker is told, in the refusal, which properties fell short.
	ErrContainment = errors.New("worker: insufficient containment")

	// ErrPlatformUnqualified reports SPEC.md §10's gate: the platform Babel is
	// running on has no sandbox backend that has passed its escape scenario, so
	// exploration is refused there whatever a worker declares. Every error
	// carrying it also matches ErrContainment, because an unqualified platform
	// is one way the boundary is insufficient; it is a sentinel of its own so
	// the operator surface can tell it apart from a worker that declares too
	// little, which is a different problem with a different remedy — one is a
	// stated limit of this platform, the other is a worker to fix.
	ErrPlatformUnqualified = errors.New("worker: platform has no qualified sandbox backend")

	// ErrEventOrder reports the resolved-configuration rule: exactly one
	// configuration event, first, before any other event. Before the
	// declaration the rule is also the staging rule — a worker that writes
	// progress, a tool request or a result in place of the declaration it
	// owes is refused there, holding the credential back — so this is the
	// error a worker that tries to be paid before it states its boundary
	// gets.
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
