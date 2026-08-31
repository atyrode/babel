package explore

import (
	"github.com/atyrode/babel/internal/presence"
)

// This file is the whole of #118's write side inside a run: two calls, both
// unable to fail, and neither of them able to change what the run does.
//
// It is separate from explore.go for the reason reference.go is: a facility
// that must never affect the run it observes is easier to keep that way when
// its entire surface is one file that returns nothing.

// announce makes this run visible to the fleet, and returns the row's id or the
// empty id when there is nothing to announce to.
//
// The empty id is not an error path. internal/presence hands back an empty id
// whenever the announcement did not land, and every later call on an empty id
// is a no-op, so an unreachable PostgreSQL costs this run one diagnostic line
// from the store's own sink and no control flow here at all.
//
// What is announced is the run's identity and its scope: the run id a receipt
// will be written under, the primary recipe, the preparation, and the authority
// that allowed it (#96). The primary recipe is one name rather than the set
// because a presence row is a status line - the receipt records every recipe
// this run applied together with its version, and that is where a reader goes
// for the full account.
func (c *Controller) announce(st *state) presence.PresenceID {
	if c.cfg.Presence == nil {
		return ""
	}
	var recipe string
	if c.cfg.Recipes != nil {
		if ids := c.cfg.Recipes.IDs(); len(ids) > 0 {
			recipe = ids[0]
		}
	}
	id, _ := c.cfg.Presence.Announce(st.ctx, presence.Announcement{
		Kind:          presence.KindExplore,
		RunID:         st.opt.RunID,
		Recipe:        recipe,
		PreparationID: string(c.cfg.Preparation.ID),
		Authority:     st.opt.Authority,
	})
	return id
}

// finalize records how this run ended and links the receipt the fleet can read
// it from.
//
// It runs on st.commit, the context detached from the run's cancellation, for
// the same reason every durable write in this package does - and here the
// reason is sharper than elsewhere. A cancelled run is precisely the case
// presence exists to report honestly: without this call the row would go stale
// and read as a machine that died, when what actually happened is that somebody
// pressed Ctrl-C and everything committed so far is durable. internal/presence
// detaches the context itself as well, so this holds even if a future caller
// hands over a cancelled one.
//
// The three states come from what the run recorded rather than from a second
// judgement: cancellation is what st.out.Cancelled means, and st.err is the
// run's own verdict, which §6.5 keeps separate from whether publication
// succeeded. So a run whose closure failed to publish finalizes as finished -
// it produced its records - and the receipt id is attached whether or not that
// receipt has published yet, because it is a record id and not a claim that the
// record is already fleet-visible.
func (c *Controller) finalize(st *state, id presence.PresenceID) {
	if c.cfg.Presence == nil || id == "" {
		return
	}
	state := presence.StateFinished
	switch {
	case st.out.Cancelled:
		state = presence.StateCancelled
	case st.err != nil:
		state = presence.StateFailed
	}
	var receipt string
	if st.out.Receipt != nil {
		receipt = string(st.out.Receipt.Header.ID)
	}
	_ = c.cfg.Presence.Finalize(st.commit, id, presence.Outcome{
		State:           state,
		ReceiptRecordID: receipt,
	})
}
