package worker

import (
	"encoding/json"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// Failure origins. The distinction is load-bearing for review: a worker that
// reported its own failure behaved correctly, while a Babel-side failure means
// the counterpart broke the contract or the supervision intervened.
const (
	FailureWorker = "worker"
	FailureBabel  = "babel"
)

// Receipt is what SPEC.md §6.5 requires this boundary to record: the profile
// reference and revision, the resolved non-secret provider metadata, the
// capability grant, every tool request with its decision, failures, resource
// use where it was observable, and timing.
//
// It deliberately cannot hold a credential. The evidence-broker token is
// scrubbed out of every worker-controlled string, tool arguments are recorded
// as a digest rather than as content, and provider credentials never reach
// Babel in the first place — Code gives them only to the OMP controller it
// supervises (SPEC.md §2.6).
type Receipt struct {
	// JobID and RunID are the job's own identifiers, echoed for correlation.
	JobID string
	RunID string

	// Profile is the Code profile the job named. The worker must resolve
	// exactly this one; a mismatch fails the run.
	Profile ProfileRef

	// Recipes and Sources echo what the job authorized: SPEC.md §6.5 requires
	// a receipt to record the cookbook policies and lens versions that ran and
	// the source digests they ran over, and §7 makes those versions part of
	// what a re-run is compared against.
	Recipes []RecipeRef
	Sources []Source

	// Worker is the counterpart's non-secret self-description, so a run can
	// be attributed to a build.
	Worker Identity

	// ProtocolVersion is the negotiated version.
	ProtocolVersion int

	// Grant is the capability boundary the run was given.
	Grant Grant

	// Privacy and Cost are the resolved profile's non-secret disclosure and
	// cost metadata, as the worker reported them.
	Privacy Privacy
	Cost    Cost

	// Containment is the sandbox the worker declared, recorded so a later
	// reviewer can see which boundary this evidence was produced behind
	// rather than assuming the boundary current at review time.
	Containment Containment

	// ResolvedCapabilities is what the worker said the profile can do. It is
	// the worker's claim, not a grant: authorization uses Grant.
	ResolvedCapabilities []Capability

	// Metadata is the resolved non-secret provider/model/thinking metadata.
	Metadata map[string]string

	// ToolRequests is every tool request the worker made, in order, with the
	// decision Babel wrote back.
	ToolRequests []ToolRecord

	// Progress is a bounded trail of progress events; ProgressDropped counts
	// the ones past the bound. A chatty worker must not make the audit record
	// unbounded.
	Progress        []ProgressRecord
	ProgressDropped int

	// Result is the run's output when it delivered one.
	Result *ResultRecord

	// Failure is the first failure, whether the worker reported it or Babel
	// detected it.
	Failure *FailureRecord

	// Resources is the worker's last self-reported resource use, or nil when
	// it reported none. Nil means unknown, never zero: SPEC.md §6.5 asks for
	// resource use "where observable", and claiming zero would be a
	// measurement Babel did not make.
	Resources *Resources

	// UnknownFields names the JSON fields Babel did not recognize, sorted.
	// They were ignored, not fatal — forward compatibility is a protocol
	// requirement — and recording their names is how an operator notices that
	// the counterpart is newer than this build.
	UnknownFields []string

	// StderrTail is a bounded, scrubbed tail of the worker's diagnostics.
	StderrTail string

	// ExitCode is the worker's exit status, or -1 when it was signalled or
	// never exited.
	ExitCode int

	// StartedAt, FinishedAt and Duration are Babel's own clock readings, not
	// the worker's: a receipt timed by the counterpart would be
	// unfalsifiable.
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// ToolRecord is one authorized-or-denied tool request.
//
// Arguments are absent by design. They can carry private locators, and a
// worker that echoes a credential into one must not be able to write it into
// Babel's durable audit record; the digest and size still let a reviewer
// correlate a request with the broker's own log.
type ToolRecord struct {
	Index           int
	RequestID       string
	Capability      Capability
	Tool            string
	ArgumentsDigest digest.Digest
	ArgumentsBytes  int
	Allowed         bool
	DenyCode        DenyCode
	Reason          string
	At              time.Time

	// Decided is how long the policy took, which is what distinguishes a slow
	// authorization from a slow worker when a run is examined afterwards.
	Decided time.Duration
}

// ProgressRecord is one progress event as recorded.
type ProgressRecord struct {
	Seq      int
	Stage    string
	Message  string
	Fraction float64
	At       time.Time
}

// ResultRecord is the run's terminal output. Payload is the worker's
// structured result, validated as JSON and scrubbed, but not interpreted:
// Babel validates structure and provenance without certifying analytical
// correctness (SPEC.md §6.5).
type ResultRecord struct {
	Status  string
	Schema  string
	Payload json.RawMessage
	At      time.Time
}

// FailureRecord is the run's first failure. Origin distinguishes the
// counterpart's own reported failure from a Babel-side supervision or
// protocol failure; Code is the codes' authority in the former case and this
// package in the latter.
type FailureRecord struct {
	Origin    string
	Code      string
	Message   string
	Retryable bool
	At        time.Time
}

// Denied reports how many tool requests were refused, which is the number a
// reviewer looks for first when a run's conclusions look thin.
func (r *Receipt) Denied() int {
	denied := 0
	for i := range r.ToolRequests {
		if !r.ToolRequests[i].Allowed {
			denied++
		}
	}
	return denied
}
