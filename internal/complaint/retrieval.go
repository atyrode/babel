package complaint

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/frontier"
)

// This file is why a complaint is worth capturing at all (#115 item 3).
//
// A complaint that only a record page showed would be a suggestion box. What
// makes it steering is that it joins the retrieval index beside Babel's own
// output, so every preparation's related-context pass surfaces the operator's
// standing annoyances as material about the work it is about to do - without a
// scheduler, a queue, or anybody remembering to look.
//
// It rides internal/index's frontier surface rather than a third one of its own.
// The surface is not "the frontier's records": it is Babel's own prose, keyed by
// (origin, kind, record id) with a plain kind column, and the one question every
// consumer asks of it is "what has this deployment already said about material
// like this". An operator's complaint is one of the answers to that question, so
// a separate index would be a second place to look for the same reason - and
// every consumer would have to learn to look in both.

// Outputs flattens the head of every complaint chain for the retrieval index.
//
// Heads only, for the reason internal/frontier gives for its own: a wording the
// operator has since amended is not what they say now, and indexing both would
// make one complaint match twice and read as two independent grievances.
//
// Nothing here is authoritative. Summary and Text are derived from the stored
// text on every read, so a consumer that caches them holds a cache and the rows
// remain the only source of truth.
//
// Subject, RunID and Status are all deliberately zero. A complaint answers about
// no record (the records answer about it), no run produced it, and it has no
// lifecycle state to report - which is #115's charter guard showing up as three
// empty fields rather than as a comment.
func (s *Store) Outputs(ctx context.Context) ([]frontier.Output, error) {
	heads, err := s.Heads(ctx)
	if err != nil {
		return nil, err
	}
	outputs := make([]frontier.Output, 0, len(heads))
	for _, c := range heads {
		outputs = append(outputs, flatten(c))
	}
	return outputs, nil
}

// Output reads one complaint as a searchable output, by id.
//
// It shares every derivation with Outputs so that the line a job document shows
// and the text an index matched cannot disagree about the same complaint. Unlike
// Outputs it does not require the complaint to be a head: a run resolving an
// identifier out of an immutable preparation is entitled to read what that
// identifier names, and telling it "amended" is the chain's business to say
// rather than this function's to hide.
func (s *Store) Output(ctx context.Context, id string) (frontier.Output, error) {
	found, err := s.Complaint(ctx, id)
	if err != nil {
		return frontier.Output{}, err
	}
	return flatten(found), nil
}

func flatten(c Complaint) frontier.Output {
	return frontier.Output{
		Kind:      frontier.OutputComplaint,
		ID:        c.ID,
		RootID:    c.RootID,
		CreatedAt: c.CreatedAt,
		Summary:   summarize(c.Text),
		Text:      c.Text,
	}
}

// Append returns outputs with every complaint head appended, which is the whole
// set the local partition of the retrieval index must be reconciled against.
//
// It exists because index.IndexFrontier takes the complete current set and
// deletes the rows that set does not name. Two call sites reconcile that
// partition - `babel prepare` and an exploration's own refresh - and a set built
// at only one of them would have each pass delete what the other indexed, so
// the complaints would flap in and out of the index depending on which command
// ran last. One function, called at both, is what keeps them describing the same
// deployment.
//
// A nil store contributes nothing and is not an error: a caller that opened no
// complaint component reconciles the frontier exactly as it did before this
// package existed.
func Append(ctx context.Context, s *Store, outputs []frontier.Output) ([]frontier.Output, error) {
	if s == nil {
		return outputs, nil
	}
	told, err := s.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read complaints for indexing: %w", err)
	}
	return append(outputs, told...), nil
}

// summarize reduces a complaint to the bounded single line a listing shows.
//
// A newline becomes a space rather than a truncation point, matching
// internal/frontier's summarizer: a complaint whose first line is "the rules" and
// whose second carries the verb would otherwise be summarized into something
// that says nothing. The cut never splits a rune, because half a rune is invalid
// UTF-8 and would reach a JSON encoder as a substitution character in the middle
// of the operator's own words.
func summarize(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if len(line) <= maxSummaryBytes {
		return line
	}
	cut := maxSummaryBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return strings.TrimRight(line[:cut], " ")
}
