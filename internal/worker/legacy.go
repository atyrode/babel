package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// legacyReceipt is the shape this boundary wrote while Babel drove Code over
// the `babel.analysis-worker` wire protocol, before #182 replaced it with
// `code engine` and native OMP RPC. The stored shape changed but the record's
// schema number did not, so receipts recorded by those runs became
// undecodable: strict decoding met three fields no current struct defines.
//
// It exists so that history stays readable. Those receipts hold the spend the
// conductor's ceilings are enforced against, the authority that says why each
// run was allowed to start, and the tool decisions review reads; a build that
// refuses to decode them does not protect anything, it silently amputates the
// record.
//
// Every field below is decoded from the same JSON key today's Receipt uses —
// this boundary's receipt has never carried json tags, so the keys are the
// exported Go names. Only the three that differ across the cutover carry a
// comment.
type legacyReceipt struct {
	JobID           string
	RunID           string
	Profile         ProfileRef
	Recipes         []RecipeRef
	Sources         []Source
	Worker          Identity
	Grant           Grant
	Privacy         Privacy
	Cost            Cost
	Containment     Containment
	Metadata        map[string]string
	ToolRequests    []ToolRecord
	Progress        []legacyProgressRecord
	ProgressDropped int
	Result          *ResultRecord
	Failure         *FailureRecord
	Resources       *legacyResources
	StderrTail      string
	ExitCode        int
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration

	// ProtocolVersion was the retired protocol's own version number. Nothing
	// in Receipt succeeds it, because the thing it versioned no longer
	// exists. It is decoded so the record is understood rather than guessed
	// at, and then deliberately not carried forward.
	ProtocolVersion int
	// ResolvedCapabilities listed the capability identifiers the worker
	// resolved for the job. Today's Receipt records registered host tool
	// names in Tools instead, which is a different vocabulary — mapping one
	// onto the other would put "corpus-search" where a tool name belongs.
	// The capability each call actually used survives on its ToolRecord, so
	// this list is read and dropped rather than mistranslated.
	ResolvedCapabilities []Capability
	// UnknownFields is UnknownFrames under its pre-cutover name: what the
	// build did not interpret, kept visible instead of silently ignored. The
	// meaning is unchanged, so it carries forward.
	UnknownFields []string
}

// legacyProgressRecord is ProgressRecord as the retired protocol carried it.
type legacyProgressRecord struct {
	Seq     int
	Stage   string
	Message string
	// Fraction was the worker's own completion estimate for the stage. The
	// native engine reports lifecycle events without one and Receipt has no
	// successor field, so it is read and dropped: a guess at how far along a
	// finished run once claimed to be is not provenance.
	Fraction float64
	At       time.Time
}

// legacyResources is Resources as the retired protocol carried it.
type legacyResources struct {
	CPUSeconds          *float64 `json:"cpu_seconds,omitempty"`
	MaxRSSBytes         *int64   `json:"max_rss_bytes,omitempty"`
	SandboxBytesWritten *int64   `json:"sandbox_bytes_written,omitempty"`
	Provenance          string   `json:"provenance,omitempty"`
	// ToolCalls counted the calls the worker served. It moved out of the
	// measured-resources record, where it never belonged — it is a count of
	// decisions, not a measurement of the process tree — and the receipt
	// still holds every one of them in ToolRequests.
	ToolCalls *int `json:"tool_calls,omitempty"`
}

// DecodeLegacyReceipt reads the worker half of a pre-cutover receipt body and
// returns it as today's Receipt. A JSON null decodes to no receipt at all,
// which is what a run that never reached the worker recorded.
//
// Decoding stays strict. Tolerating the fields the cutover retired is not the
// same as tolerating any field: one is this build's own history, the other is
// a row that was altered outside Babel.
func DecodeLegacyReceipt(raw []byte) (*Receipt, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var l legacyReceipt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("worker: decode pre-cutover receipt: %w", err)
	}
	return &Receipt{
		JobID:           l.JobID,
		RunID:           l.RunID,
		Profile:         l.Profile,
		Recipes:         l.Recipes,
		Sources:         l.Sources,
		Worker:          l.Worker,
		Grant:           l.Grant,
		Privacy:         l.Privacy,
		Cost:            l.Cost,
		Containment:     l.Containment,
		Metadata:        l.Metadata,
		ToolRequests:    l.ToolRequests,
		Progress:        legacyProgress(l.Progress),
		ProgressDropped: l.ProgressDropped,
		Result:          l.Result,
		Failure:         l.Failure,
		Resources:       legacyResourceUse(l.Resources),
		UnknownFrames:   l.UnknownFields,
		StderrTail:      l.StderrTail,
		ExitCode:        l.ExitCode,
		StartedAt:       l.StartedAt,
		FinishedAt:      l.FinishedAt,
		Duration:        l.Duration,
	}, nil
}

func legacyProgress(records []legacyProgressRecord) []ProgressRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]ProgressRecord, len(records))
	for i, r := range records {
		out[i] = ProgressRecord{Seq: r.Seq, Stage: r.Stage, Message: r.Message, At: r.At}
	}
	return out
}

func legacyResourceUse(r *legacyResources) *Resources {
	if r == nil {
		return nil
	}
	return &Resources{
		CPUSeconds:          r.CPUSeconds,
		MaxRSSBytes:         r.MaxRSSBytes,
		SandboxBytesWritten: r.SandboxBytesWritten,
		Provenance:          r.Provenance,
	}
}
