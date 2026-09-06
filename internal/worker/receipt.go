package worker

import (
	"encoding/json"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// Failure origins. The distinction is load-bearing for review: an engine that
// exited with its own failure status behaved as a process should, while a
// Babel-side failure means the boundary was broken or the supervision
// intervened.
const (
	FailureWorker = "worker"
	FailureBabel  = "babel"
)

// Receipt is what SPEC.md §6.5 requires this boundary to record: the profile
// reference and revision, the resolved non-secret provider metadata, the
// capability grant, every tool call with its decision, failures, resource use
// where it was observable, and timing.
//
// It deliberately cannot hold a credential. No credential travels through
// Babel at all — Code holds the provider's and gives it to the engine it
// supervises (SPEC.md §2.6), and the evidence tools answer over the pipe with
// no token of their own — and tool arguments are recorded as a digest rather
// than as content. Evidence a tool served is never here either: the receipt
// records the decision and the retrieval trace's locators, and §9 forbids the
// durable record becoming a plaintext store of archive content.
type Receipt struct {
	// JobID and RunID are the job's own identifiers, echoed for correlation.
	JobID string
	RunID string

	// Profile is the Code profile the job named and the launch ran under.
	Profile ProfileRef

	// Recipes and Sources echo what the job authorized: SPEC.md §6.5 requires
	// a receipt to record the cookbook policies and lens versions that ran and
	// the source digests they ran over, and §7 makes those versions part of
	// what a re-run is compared against.
	Recipes []RecipeRef
	Sources []Source

	// Worker is Code's non-secret self-description from the runtime-info
	// sidecar, so a run can be attributed to a build.
	Worker Identity

	// Grant is the capability boundary the run was given.
	Grant Grant

	// Privacy and Cost are the resolved profile's non-secret disclosure and
	// cost metadata, as Code reported them.
	Privacy Privacy
	Cost    Cost

	// Containment is the sandbox Code declared it launched the engine into,
	// recorded so a later reviewer can see which boundary this evidence was
	// produced behind rather than assuming the boundary current at review
	// time.
	Containment Containment

	// Metadata is the profile's non-secret provider metadata. Babel refuses
	// a launch whose metadata names a credential, so nothing here is one.
	Metadata map[string]string

	// Tools are the host tool names Babel registered for this job, in the
	// order the engine confirmed them. The submit tool is always among them.
	Tools []string

	// ToolRequests are every host tool call the engine made, in order, with
	// Babel's decision on each. Submissions are among them, under ToolSubmit.
	ToolRequests []ToolRecord

	// Progress is the bounded record of the engine's lifecycle events;
	// ProgressDropped counts what the bound excluded.
	Progress        []ProgressRecord
	ProgressDropped int

	// Submissions counts every call to the submit tool, accepted or not.
	// Result is the last accepted one; a refused submission never replaces
	// it.
	Submissions int
	Result      *ResultRecord

	// Failure is the first failure of the run, from whichever side.
	Failure *FailureRecord

	// Resources are Code's measurements of the engine's process tree, from
	// the finished runtime-info report. Nil when Code wrote none.
	Resources *Resources

	// Usage is the engine's own session accounting, when it answered
	// get_session_stats before the run ended. Nil when it did not.
	Usage *Usage

	// AssistantMessages and Fallbacks retain received native accounting
	// facts independently of the progress bound. Nil/empty means no such
	// observation was received, not proof of no model work or no fallback.
	// Older receipts and runs ending before these events are unavailable.
	// These are an observed stream prefix on failure, not a complete native
	// session history; the run's Failure describes interrupted supervision.
	AssistantMessages []AssistantMessageAccounting
	Fallbacks         []FallbackRecord

	// UnknownFrames lists the stdout frame types this build did not
	// interpret, so a newer engine's additions are visible rather than
	// silently ignored. Frame types only, never content.
	UnknownFrames []string

	// StderrTail is the bounded tail of the engine's diagnostics.
	StderrTail string

	// ExitCode is the engine's exit status as Code reported it in the
	// finished report, or Code's own when it wrote none; -1 when the tree
	// was killed.
	ExitCode int

	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// ToolRecord is one host tool call and Babel's decision on it. Arguments are
// digested rather than stored: a query can carry material a run is not cleared
// to persist, and the digest still proves what was asked.
type ToolRecord struct {
	Index int
	// RequestID is the engine's host_tool_call id; ToolCallID is the model's
	// own tool-call identifier, which the transcript keys the call by.
	RequestID  string
	ToolCallID string
	// Capability is the capability the tool serves, empty for the submit
	// tool and for a call to a name the job did not register.
	Capability      Capability
	Tool            string
	ArgumentsDigest digest.Digest
	ArgumentsBytes  int
	Allowed         bool
	DenyCode        DenyCode
	Reason          string
	At              time.Time
	Decided         time.Duration
}

// ProgressRecord is one engine lifecycle event, kept as a stage name and a
// short message rather than the event itself: message deltas carry model
// text, and a receipt is not a transcript.
type ProgressRecord struct {
	Seq     int
	Stage   string
	Message string
	At      time.Time
}

// ResultRecord is the last accepted submission.
type ResultRecord struct {
	// Status is StatusOK when the run ended with this submission accepted.
	Status string
	// Schema is the job's result schema identifier.
	Schema string
	// Payload is the submission's arguments, byte for byte, as the engine
	// validated them against the schema and the job accepted them.
	Payload json.RawMessage
	At      time.Time
}

// Result statuses. A run that ended without an accepted submission has no
// ResultRecord at all rather than a partial one: emitting nothing is an
// outcome the caller decides about, and inventing a status for it here would
// be Babel's control plane describing a result nobody wrote.
const StatusOK = "ok"

// FailureRecord is one failure with its origin.
type FailureRecord struct {
	Origin    string
	Code      string
	Message   string
	Retryable bool
	At        time.Time
}

// Denied counts the tool calls Babel refused.
func (r *Receipt) Denied() int {
	n := 0
	for _, t := range r.ToolRequests {
		if !t.Allowed {
			n++
		}
	}
	return n
}
