package conductor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/run"
)

// Ceilings are the operator's stated limits on autonomy.
//
// #96 puts it exactly: autonomy is budget-bounded, not trust-bounded. Both
// ceilings are mandatory and neither has a default, because a default ceiling is
// a limit nobody chose — and the whole difference between a bounded loop and an
// unbounded one is that a person said the number out loud.
type Ceilings struct {
	// Currency names the unit both ceilings are quoted in. A receipt whose
	// profile reported a different currency is counted as unpriced rather than
	// converted: Babel holds no exchange rate and inventing one would make a
	// ceiling enforceable only by accident.
	Currency string
	// PerCycle is the most one cycle may cost.
	PerCycle float64
	// PerDay is the most every cycle in one UTC day may cost together.
	PerDay float64
}

func (c Ceilings) validate() error {
	if c.Currency == "" {
		return errors.New("conductor: the ceilings name no currency, so nothing can be compared against them")
	}
	if c.PerCycle <= 0 || c.PerDay <= 0 {
		return errors.New("conductor: a conductor refuses to run without explicit per-cycle and per-day ceilings")
	}
	if c.PerCycle > c.PerDay {
		return errors.New("conductor: a per-cycle ceiling above the per-day ceiling would refuse every cycle")
	}
	return nil
}

// Spend is what a day's receipts already estimated.
type Spend struct {
	// Amount is the estimated cost of the day's runs, in the ceiling's
	// currency.
	Amount float64
	// Runs is how many receipts contributed to Amount.
	Runs int
	// Unpriced counts the day's receipts whose profile reported no usable cost:
	// no currency, or a currency that is not the one the ceilings are in.
	//
	// It is reported rather than folded into the total because a run that
	// cannot be priced is not a free run. An operator seeing spend well under
	// the ceiling beside a non-zero unpriced count knows the ceiling is
	// bounding less than it appears to, which is a true and actionable thing to
	// know; a zero would have been a claim.
	Unpriced int
}

// Remaining reports what is left of the day's ceiling. It can be negative,
// which is a real state: a cycle that overran cannot be un-run.
func (s Spend) Remaining(c Ceilings) float64 { return c.PerDay - s.Amount }

// refuse decides whether the next cycle may start.
//
// The test is not "have we already overspent" but "can the next cycle fit": the
// per-cycle ceiling is the operator's own statement of what one cycle may cost,
// so it is the honest worst case for a cycle that has not run yet. Starting a
// cycle Babel could not afford to finish and discovering the overrun from its
// receipt would make the day ceiling advisory.
func (c Ceilings) refuse(s Spend) (string, bool) {
	if s.Amount >= c.PerDay {
		return fmt.Sprintf("today's runs have already estimated %.2f %s, at or over the %.2f %s daily ceiling",
			s.Amount, c.Currency, c.PerDay, c.Currency), true
	}
	if s.Amount+c.PerCycle > c.PerDay {
		return fmt.Sprintf("today's %.2f %s of the %.2f %s daily ceiling leaves %.2f %s, less than the %.2f %s a cycle may cost",
			s.Amount, c.Currency, c.PerDay, c.Currency, s.Remaining(c), c.Currency,
			c.PerCycle, c.Currency), true
	}
	return "", false
}

// overrun reports a completed cycle that cost more than one cycle may.
//
// It parks rather than warns. The ceiling is a statement about what a single
// cycle is worth, so the first cycle to break it is evidence that the next one
// will too, and a loop that noted the breach and carried on would be spending
// past the operator's limit one cycle at a time.
func (c Ceilings) overrun(cycle Cycle) (bool, string) {
	if cycle.Currency != c.Currency || cycle.Cost <= c.PerCycle {
		return false, ""
	}
	return true, fmt.Sprintf("cycle %d estimated %.2f %s, over the %.2f %s per-cycle ceiling",
		cycle.Seq, cycle.Cost, cycle.Currency, c.PerCycle, c.Currency)
}

// ReceiptLedger sums the day's spend from the run receipts.
//
// The receipts are the only honest source. A conductor that added up its own
// cycles would be enforcing a ceiling against its own memory, would miss every
// run an operator started by hand, and would forget the whole day on restart.
type ReceiptLedger struct {
	runs Runs
}

// NewReceiptLedger builds a ledger over the run receipt store.
func NewReceiptLedger(runs Runs) *ReceiptLedger { return &ReceiptLedger{runs: runs} }

// SpentSince sums the estimated cost of every receipt recorded at or after
// since, in currency.
//
// The figure is an estimate and is treated as one: it is the profile's own cost
// metadata as the worker reported it (§2.6 keeps the provider inside Code), not
// a measurement Babel made. That is exactly what a ceiling can be enforced
// against — Babel never sees an invoice — and it is why the unpriced count is
// carried alongside rather than silently absorbed.
//
// Amendments do not double-count: only the newest revision of each run is
// listed, which is the same rule a receipt listing follows.
func (l *ReceiptLedger) SpentSince(ctx context.Context, since time.Time, currency string) (Spend, error) {
	var spend Spend
	since = since.UTC()
	for offset := 0; ; offset += run.MaxListLimit {
		receipts, total, err := l.runs.Receipts(ctx, run.MaxListLimit, offset)
		if err != nil {
			return Spend{}, fmt.Errorf("conductor: list receipts: %w", err)
		}
		if len(receipts) == 0 {
			return spend, nil
		}
		for _, receipt := range receipts {
			// Newest first, so the first receipt older than the window ends
			// the walk rather than merely being skipped.
			if receipt.Header.RecordedAt.Before(since) {
				return spend, nil
			}
			cost, priced := estimatedCost(receipt, currency)
			if !priced {
				spend.Unpriced++
				continue
			}
			spend.Amount += cost
			spend.Runs++
		}
		if offset+len(receipts) >= total {
			return spend, nil
		}
	}
}

// estimatedCost reads one receipt's estimated cost in currency, reporting
// whether it could be priced at all.
//
// A receipt with no worker boundary reached no model and cost nothing, which is
// a priced zero rather than an unknown: the run is accounted for. A receipt
// whose profile quoted no currency, or another one, is unpriced — Babel shows a
// figure only when it has a unit for it, and the same rule that stops a cost
// being displayed wrongly stops it being enforced wrongly.
func estimatedCost(receipt run.Receipt, currency string) (float64, bool) {
	if receipt.Body.Worker == nil {
		return 0, true
	}
	cost := receipt.Body.Worker.Cost
	if cost.Currency != currency {
		return 0, false
	}
	if cost.EstimatedRun < 0 {
		return 0, false
	}
	return cost.EstimatedRun, true
}
