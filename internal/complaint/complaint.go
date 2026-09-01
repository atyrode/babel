// Package complaint holds the operator's own steering input: an open-ended
// sentence about what is going badly, captured as a first-class Phase B record
// (issue #115).
//
// # WHY IT IS A RECORD AND NOT A TICKET
//
// Everything else Babel stores it produced. A hypothesis is a claim a run
// minted, a finding is one it consolidated, a disposition is an operator
// answering something Babel put in front of them. This package is the one
// place where the operator speaks first - "I am having a hard time enforcing
// my repository rules" - and it is deliberately the smallest record in the
// corpus: some text, who wrote it, on which machine, when.
//
// What is absent is the whole design. There is no state, no assignee, no
// priority, no due date, no closure and no resolved flag, because the moment a
// complaint acquires one Babel has become a work tracker and GitHub already is
// one (#115's charter guard). "Was this ever addressed?" is not a column here:
// it is a backlink query over the typed reference graph (#113), answered by the
// `addresses` edges that hypotheses and proposals mint towards a complaint.
// The absence of an answer is then visibly the absence of work rather than a
// field nobody updated.
//
// # WHAT A COMPLAINT IS EPISTEMICALLY
//
// A want, never a fact. It is operator-authored, so it is authorized at birth
// as *steering* (the intentionality principle, #86) - Babel may act on it
// without asking - but it enters no reality ledger: memory holds authorized
// facts, and "I am having a hard time" is a report of an experience, not an
// established property of the world. Investigating one may propose facts
// through the existing proposed-until-authorized flow, and that is the only
// route from a complaint to the ledger.
//
// # AMENDING, NOT CLOSING
//
// A complaint has a revision chain for the same reason every other record does
// (#87): the operator may say it better later, and the earlier wording is
// history rather than error. Amending appends; nothing is deleted, nothing is
// overwritten, and a chain has exactly one head, which the database enforces
// rather than the caller. There is deliberately no transition that ends a
// chain: a complaint at rest is a complaint nobody has acted on yet, which is
// information, and a "closed" one would be a claim this package cannot check.
package complaint

import (
	"errors"
	"time"
)

// RecordSchema is the version stamped on every row this build writes, on the
// same terms as frontier.RecordSchema and disposition.RecordSchema: §9
// requires a reader to be able to tell which shape it is decoding.
const RecordSchema = 1

// MaxTextBytes bounds one complaint's text.
//
// It is generous on purpose. A complaint is the operator writing prose at the
// moment they are annoyed, and a bound that cut them off mid-thought would
// train them to file terse ones - which are exactly the complaints nothing can
// be done with. It exists at all because this text is sealed into an immutable
// object and indexed for retrieval, and neither of those should have to
// discover its size at the far end.
const MaxTextBytes = 16 << 10

// maxSummaryBytes bounds the one-line summary a listing and a search hit show.
//
// It matches internal/frontier's bound for its own outputs because both fill
// the same column of the same retrieval index: a complaint that summarized
// itself at a different length would render inconsistently beside the records
// that answer it, for no reason a reader could see.
const maxSummaryBytes = 240

// Sentinel errors callers are expected to handle rather than merely report.
var (
	// ErrInvalidValue reports a value this package refuses: empty text, an
	// unattributed capture, prose past MaxTextBytes.
	ErrInvalidValue = errors.New("invalid value")
	// ErrUnknownComplaint reports a reference to a complaint this store does
	// not hold. Nothing here is ever deleted, so it always means the
	// reference was wrong rather than that the complaint went away.
	ErrUnknownComplaint = errors.New("unknown complaint")
	// ErrSuperseded reports an amendment against a wording that has already
	// been amended. A chain has one head, and an operator amending an older
	// revision is working from a stale view rather than branching the chain.
	ErrSuperseded = errors.New("complaint is already amended; amend the chain's head")
)

// Complaint is one thing the operator said, at one wording.
//
// It is a revision, not a subject: amending appends a second Complaint sharing
// this one's RootID, and the two are the same complaint said twice. A caller
// that wants "what does the operator currently say" reads the head; one that
// wants "what have they said about this" reads the chain.
type Complaint struct {
	ID string
	// RootID is the chain identity - the id of the first wording - so a
	// citation of any revision can be recognized as touching the same
	// complaint. It is the record's own id for an original.
	RootID string
	// AncestorID is the wording this one amends, empty for an original.
	AncestorID string
	// Sequence is the record's position in its chain, starting at 1. It is
	// what makes "the operator said this three amendments ago" orderable
	// without comparing timestamps a clock could have skewed.
	Sequence int
	// By is the operator. A complaint outranks the conductor's own policy at
	// the invitation rung of #96's ladder, so an unattributed one would be a
	// way of borrowing operator authority without an operator.
	By string
	// Host is the machine the complaint was captured on. It is provenance and
	// never scope: a complaint told on the laptop is the fleet's complaint,
	// and this only answers "where was I when I said it".
	Host string
	// Text is what the operator wrote, after secret redaction. It is the only
	// content this record carries and the only field that is sealed on
	// publication.
	Text string
	// Redacted reports that capture replaced secret-shaped material in the
	// text with stable placeholders.
	//
	// It is recorded rather than merely reported at the terminal because the
	// stored text then visibly differs from what the operator typed, and a
	// reader who cannot tell that a placeholder was Babel's doing would read
	// it as something the operator wrote.
	Redacted  bool
	CreatedAt time.Time
}

// payload is the §9 encryption-bound half of a complaint row: the operator's
// words and nothing else. Everything beside it in the table is identifier or
// attribution metadata the plaintext allowlist admits.
type payload struct {
	Text string `json:"text"`
}
