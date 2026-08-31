package omp

// Usage extraction for the OMP adapter (issue #89).
//
// OMP writes a usage block into every assistant record it appends to a
// session log: the turn's input, output and cache token counts, the total
// the provider reported, the priced cost of each, the model and the
// provider that served it. The same numbers also land in OMP's own
// ~/.omp/stats.db, keyed by session file and entry id - which is exactly
// the path Babel ingested and the turn ids inside it, a free and exact
// join. Babel does not take it, and the reason is retention: that ledger
// is garbage collected with OMP's local session retention, so it answers
// for recent sessions and knows nothing about the ones Babel archived a
// year ago. The transcript Babel already holds is durable and covers the
// whole corpus, so the aggregate is recomputed from it and the ledger is
// left as an optional cross-check.
//
// The scan is therefore a second pass over the primary log, arithmetic
// only: it sums what OMP wrote and never prices, estimates, or infers
// anything. A record it cannot read is counted and skipped, because
// restic's snapshots are crash-consistent per file rather than
// transactional across them and a torn tail must degrade an aggregate
// instead of failing a description.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/atyrode/babel/internal/adapter"
)

const (
	// usageReadBuffer is the window one record is read through. Records are
	// a few hundred bytes to a few kilobytes; the megabyte window is for
	// the outliers - a tool result carrying a whole file - so those are
	// assembled in one or two appends rather than hundreds.
	usageReadBuffer = 1 << 20
	// usageRecordLimit bounds one assembled record so a describe's memory
	// stays independent of a log that grew to hundreds of megabytes. The
	// largest record in the operator's live corpus is 3.5 MB, so this is
	// an order of magnitude of headroom; a record past it is counted as
	// unreadable rather than assembled.
	usageRecordLimit = 32 << 20
	// usageCancelInterval is how many records pass between context checks.
	// A cancelled describe must not read a 300 MB log to its end, and
	// checking every record would pay a lock per line for that.
	usageCancelInterval = 1024
)

// Record types and roles this scan reads. They are OMP's own vocabulary,
// named here so the scan's reach is a short list rather than a set of
// string literals buried in a switch.
const (
	messageRecord  = "message"
	assistantRole  = "assistant"
	toolResultRole = "toolResult"
	toolCallBlock  = "toolCall"
)

// The reasons an aggregate is absent. Each one names what the scan
// actually observed, because "no usage" has three different causes and
// only one of them is a statement about the session.
const (
	noUsageReason    = "the session log's assistant records carry no usage blocks, which is what a transcript written before OMP recorded per-turn usage looks like"
	unreadableReason = "the session log could not be opened for the usage scan"
	// unparsedReasonFmt covers the case that would otherwise lie: no usage
	// was found and records could not be read either, so the log may well
	// have recorded usage in a shape this scan no longer understands.
	// Reporting it as a session that recorded none would turn a reader
	// bug into a fact about the transcript.
	unparsedReasonFmt = "no assistant record carried a readable usage block, and %d record(s) of this log could not be parsed at all"
)

// usageRecord decodes exactly the fields of one log record the aggregate
// needs. Every other key - the message text, a tool call's arguments, a
// tool result's whole payload - is skipped by encoding/json without being
// materialized, which is what makes a full pass over a multi-hundred
// megabyte log cost a scan rather than its weight in garbage.
type usageRecord struct {
	Type    string `json:"type"`
	Message struct {
		Role     string     `json:"role"`
		Model    string     `json:"model"`
		Provider string     `json:"provider"`
		IsError  bool       `json:"isError"`
		Usage    *turnUsage `json:"usage"`
		Content  []struct {
			Type string `json:"type"`
		} `json:"content"`
	} `json:"message"`
}

// turnUsage is one assistant turn's usage block.
type turnUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
	// Cost is absent for a turn the harness did not price, which is a
	// different state from a turn priced at zero: a pointer keeps them
	// apart so TurnsWithCost can say how much of CostUSD is accounted for.
	Cost *struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// scanUsage aggregates one session log's recorded usage.
//
// It returns the aggregate, or nil together with the reason there is none.
// A file that cannot be opened and a log that records no usage are both
// absences with a reason rather than errors: a description is a
// best-effort view of live files. Only cancellation is an error, because a
// cancelled scan holds a partial sum and reporting one as a session's
// spend would be worse than reporting nothing.
func scanUsage(ctx context.Context, path string) (*adapter.Usage, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, unreadableReason, nil
	}
	defer f.Close()

	agg := usageAggregate{}
	rr := &recordReader{r: bufio.NewReaderSize(f, usageReadBuffer)}
	for n := 0; ; n++ {
		if n%usageCancelInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		record, tooLong, readErr := rr.next()
		switch {
		case tooLong:
			agg.usage.UnreadableRecords++
		case len(bytes.TrimSpace(record)) > 0:
			agg.add(record)
		}
		if readErr != nil {
			break
		}
	}

	if agg.usage.TurnsWithUsage == 0 {
		if n := agg.usage.UnreadableRecords; n > 0 {
			return nil, fmt.Sprintf(unparsedReasonFmt, n), nil
		}
		return nil, noUsageReason, nil
	}
	return agg.finish(), "", nil
}

// usageAggregate accumulates the running totals of one scan.
type usageAggregate struct {
	usage     adapter.Usage
	models    map[string]struct{}
	providers map[string]struct{}
}

// add folds one record into the totals. A record that is not JSON, or not
// one of the two shapes this scan reads, contributes nothing: an
// unparsable record is counted so the aggregate can admit its holes, while
// a well-formed record of another type is simply not this scan's business.
func (a *usageAggregate) add(record []byte) {
	var rec usageRecord
	if err := json.Unmarshal(record, &rec); err != nil {
		a.usage.UnreadableRecords++
		return
	}
	if rec.Type != messageRecord {
		return
	}
	switch rec.Message.Role {
	case assistantRole:
		a.usage.AssistantTurns++
		for _, block := range rec.Message.Content {
			if block.Type == toolCallBlock {
				a.usage.ToolCalls++
			}
		}
		if rec.Message.Usage == nil {
			return
		}
		u := rec.Message.Usage
		a.usage.TurnsWithUsage++
		a.usage.InputTokens += u.Input
		a.usage.OutputTokens += u.Output
		a.usage.CacheReadTokens += u.CacheRead
		a.usage.CacheWriteTokens += u.CacheWrite
		a.usage.TotalTokens += u.TotalTokens
		if u.Cost != nil {
			a.usage.TurnsWithCost++
			a.usage.CostUSD += u.Cost.Total
		}
		// The model and provider sets are collected from the turns whose
		// usage was counted, so they name what actually served the tokens
		// summed here rather than every model the log happens to mention.
		a.models = addTo(a.models, rec.Message.Model)
		a.providers = addTo(a.providers, rec.Message.Provider)
	case toolResultRole:
		if rec.Message.IsError {
			a.usage.ToolErrors++
		}
	}
}

// finish renders the accumulated totals, with the model and provider sets
// sorted so two scans of the same bytes produce the same document.
func (a *usageAggregate) finish() *adapter.Usage {
	out := a.usage
	out.Models = sortedKeys(a.models)
	out.Providers = sortedKeys(a.providers)
	return &out
}

func addTo(set map[string]struct{}, value string) map[string]struct{} {
	if value == "" {
		return set
	}
	if set == nil {
		set = make(map[string]struct{}, 2)
	}
	set[value] = struct{}{}
	return set
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// recordReader reads newline-delimited records, reusing one buffer across
// them so memory is bounded by the longest record assembled rather than by
// the log's size.
type recordReader struct {
	r   *bufio.Reader
	buf []byte
}

// next returns the next record's bytes, valid only until the following
// call, together with the read error that ended the log.
//
// A record longer than usageRecordLimit is reported as tooLong and drained
// to its newline, so the scan resumes at the next record instead of
// mistaking the remainder of an outsized line for records of its own.
func (rr *recordReader) next() (record []byte, tooLong bool, err error) {
	rr.buf = rr.buf[:0]
	over := false
	for {
		slice, err := rr.r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			if over || len(rr.buf)+len(slice) > usageRecordLimit {
				// Past the limit nothing more is retained; the remaining
				// slices of this line are read and dropped.
				over = true
				rr.buf = rr.buf[:0]
				continue
			}
			rr.buf = append(rr.buf, slice...)
			continue
		}
		if over {
			return nil, true, err
		}
		if len(rr.buf) == 0 {
			// The whole record fit the read window: it is returned in
			// place, and the scan parses it before the next read.
			return slice, false, err
		}
		rr.buf = append(rr.buf, slice...)
		return rr.buf, false, err
	}
}
