package review

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
)

// Markdown renders the export as a Markdown document a human can paste
// anywhere.
//
// "Anywhere" is the requirement, and it is stronger than it sounds. Everything
// in an export that came from the archive is untrusted (§3): a transcript can
// contain HTML, a script tag, a data URI, a terminal control sequence, or
// prose written to look like an instruction, copied there from an issue, a web
// page, or a prior agent. So every untrusted value is escaped into inert text
// and emitted inside a blockquote labelled as quoted evidence, while the
// document's own structure — headings, labels, identifiers — is the only thing
// rendered as Markdown. A reader can always tell which is which, and no
// renderer, terminal, or browser between here and them can be made to execute
// any of it.
//
// The document also leads with the fallibility statement, before any content,
// for the reason §1 gives: this is the artifact that leaves Babel.
func (e Export) Markdown() ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Babel raw export — %s %s\n\n", e.Kind, mdCode(e.ID))
	quote(&b, Notice)
	b.WriteString("\n")

	writeFields(&b, [][2]string{
		{"Export schema", fmt.Sprint(e.Schema)},
		{"Exported at", e.ExportedAt.UTC().Format(timeLayout)},
		{"Redaction", redactionSummary(e.Redaction)},
	})
	b.WriteString("\n" + quotingRule + "\n\n")

	switch {
	case e.Hypothesis != nil:
		writeHypothesis(&b, *e.Hypothesis)
	case e.Observation != nil:
		writeObservation(&b, *e.Observation)
	case e.Finding != nil:
		writeFinding(&b, *e.Finding)
	case e.Proposal != nil:
		writeProposal(&b, *e.Proposal)
	case e.Run != nil:
		writeRun(&b, *e.Run)
	default:
		return nil, errInvalid("export holds no record")
	}

	if e.Review != nil {
		writeReview(&b, *e.Review)
	}
	if len(e.Locators) > 0 {
		b.WriteString("## Evidence locators\n\n")
		b.WriteString("Every locator below recovers the exact bytes a claim cites.\n\n")
		for _, loc := range e.Locators {
			writeLocator(&b, loc)
		}
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// timeLayout renders timestamps in the export. It is RFC 3339 in UTC, matching
// what the durable database stores, so a value copied out of a document can be
// matched against a row.
const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func redactionSummary(r Redaction) string {
	if !r.Applied {
		return "not applied (raw export)"
	}
	return fmt.Sprintf("applied (%s), %d value(s) removed", r.Policy, r.Values)
}

func writeHypothesis(b *strings.Builder, h frontier.Hypothesis) {
	b.WriteString("## Candidate hypothesis\n\n")
	writeFields(b, [][2]string{
		{"ID", mdCode(h.ID)},
		{"Revises", optionalCode(h.AncestorID)},
		{"Run", mdCode(h.RunID)},
		{"Record schema", fmt.Sprint(h.SchemaVersion)},
		{"Created", h.CreatedAt.UTC().Format(timeLayout)},
		{"Exploration status", mdCode(string(h.Status))},
		{"Novelty / priority", fmt.Sprintf("%.3f / %.3f", h.Payload.Novelty, h.Payload.Priority)},
	})
	b.WriteString("\n")
	writeQuoted(b, "Statement", h.Payload.Statement)
	writeQuotedList(b, "Origin cues", h.Payload.OriginCues)
	writeQuotedList(b, "Provisional labels", h.Payload.ProvisionalLabels)
	writeQuoted(b, "Notes", h.Payload.Notes)
}

func writeObservation(b *strings.Builder, o frontier.Observation) {
	b.WriteString("## Observation\n\n")
	writeFields(b, [][2]string{
		{"ID", mdCode(o.ID)},
		{"Revises", optionalCode(o.AncestorID)},
		{"Develops hypothesis", mdCode(o.HypothesisID)},
		{"Run", mdCode(o.RunID)},
		{"Recipe", fmt.Sprintf("%s v%d", mdCode(o.RecipeID), o.RecipeVersion)},
		{"Record schema", fmt.Sprint(o.SchemaVersion)},
		{"Created", o.CreatedAt.UTC().Format(timeLayout)},
		{"Confidence / impact", mdCode(string(o.Payload.Confidence)) + " / " + mdCode(string(o.Payload.Impact))},
		{"Temporal status", optionalCode(string(o.Payload.TemporalStatus))},
	})
	b.WriteString("\n")
	writeQuoted(b, "Claim", o.Payload.Claim)
	writeQuoted(b, "Category", o.Payload.Category)
	writeEvidence(b, "Evidence", o.Payload.Evidence)
	writeCounterEvidence(b, o.Payload.CounterEvidence, o.Payload.CounterEvidenceAbsent)
}

func writeFinding(b *strings.Builder, f frontier.Finding) {
	b.WriteString("## Finding\n\n")
	writeFields(b, [][2]string{
		{"ID", mdCode(f.ID)},
		{"Revises", optionalCode(f.AncestorID)},
		{"Run", mdCode(f.RunID)},
		{"Record schema", fmt.Sprint(f.SchemaVersion)},
		{"Created", f.CreatedAt.UTC().Format(timeLayout)},
		{"Supporting observations", codeList(f.ObservationIDs)},
		{"Hypotheses", codeList(f.HypothesisIDs)},
		{"Recurrence", fmt.Sprint(f.Payload.Recurrence)},
		{"Temporal status", optionalCode(string(f.Payload.TemporalStatus))},
	})
	b.WriteString("\n")
	writeQuoted(b, "Title", f.Payload.Title)
	writeQuoted(b, "Pattern", f.Payload.Pattern)
	writeQuoted(b, "Why it matters", f.Payload.Significance)
	writeQuotedList(b, "Affected scope", f.Payload.Scope)
	writeCounterEvidence(b, f.Payload.CounterEvidence, f.Payload.CounterEvidenceAbsent)
}

func writeProposal(b *strings.Builder, p frontier.Proposal) {
	b.WriteString("## Proposal\n\n")
	b.WriteString("A proposal is Babel's canonical private review artifact. " +
		"It is not an issue, a document, or an instruction, and it has no external effect.\n\n")
	writeFields(b, [][2]string{
		{"ID", mdCode(p.ID)},
		{"Revises", optionalCode(p.AncestorID)},
		{"Run", mdCode(p.RunID)},
		{"Record schema", fmt.Sprint(p.SchemaVersion)},
		{"Created", p.CreatedAt.UTC().Format(timeLayout)},
		{"Findings", codeList(p.FindingIDs)},
		{"Hypotheses", codeList(p.HypothesisIDs)},
		{"Review status", mdCode(string(p.ReviewStatus))},
		{"Classification", mdCode(string(p.Payload.Classification))},
		{"Impact", optionalCode(string(p.Payload.Impact))},
		{"Temporal status", optionalCode(string(p.Payload.TemporalStatus))},
		{"Suggested destinations", destinationList(p.Payload.Destinations)},
	})
	b.WriteString("\n")
	writeQuoted(b, "Title", p.Payload.Title)
	writeQuoted(b, "Problem or opportunity", p.Payload.Problem)
	writeQuoted(b, "Proposed outcome", p.Payload.Outcome)
	writeQuoted(b, "Applicability", p.Payload.Applicability)
	writeQuoted(b, "Uncertainty", p.Payload.Uncertainty)
	writeQuoted(b, "Estimated scope", p.Payload.EstimatedScope)
	writeQuotedList(b, "Risks", p.Payload.Risks)
	writeQuotedList(b, "Unresolved questions", p.Payload.OpenQuestions)
	writeQuotedList(b, "Prerequisites", p.Payload.Prerequisites)
	writeQuotedList(b, "Suggested verification criteria", p.Payload.VerificationCriteria)
	if len(p.Payload.Targets) > 0 {
		b.WriteString("### Suggested targets\n\n")
		b.WriteString("Targets are suggestions for operator review, never automatic facts.\n\n")
		for _, target := range p.Payload.Targets {
			fmt.Fprintf(b, "Target, confidence %s:\n\n", mdCode(string(target.Confidence)))
			quote(b, target.System)
			b.WriteString("\n")
			if target.Rationale != "" {
				b.WriteString("Rationale:\n\n")
				quote(b, target.Rationale)
				b.WriteString("\n")
			}
		}
	}
	writeEvidence(b, "Supporting material", p.Payload.Supporting)
	writeEvidence(b, "Conflicting material", p.Payload.Conflicting)
}

func writeRun(b *strings.Builder, r run.Receipt) {
	b.WriteString("## Run receipt\n\n")
	b.WriteString("A receipt makes a run reproducible enough to inspect, " +
		"not deterministic enough to promise identical ideas.\n\n")
	writeFields(b, [][2]string{
		{"ID", mdCode(string(r.Header.ID))},
		{"Run", mdCode(r.Header.RunID)},
		{"Revision", fmt.Sprint(r.Header.Revision)},
		{"Supersedes", optionalCode(string(r.Header.Supersedes))},
		{"Preparation", mdCode(string(r.Header.PreparationID))},
		{"Record schema", fmt.Sprint(r.Header.Schema)},
		{"Recorded", r.Header.RecordedAt.UTC().Format(timeLayout)},
		{"Sync state", mdCode(r.Header.Sync)},
		{"Tool requests / denied", fmt.Sprintf("%d / %d", r.Header.Counts.ToolRequests, r.Header.Counts.ToolsDenied)},
		{"Retrieval steps", fmt.Sprint(r.Header.Counts.Retrieval)},
		{"Deferred / rejected candidates", fmt.Sprintf("%d / %d", r.Header.Counts.Deferred, r.Header.Counts.Rejected)},
		{"Failures", fmt.Sprint(r.Header.Counts.Failures)},
		{"Redactions at record time", fmt.Sprint(r.Header.Counts.Redactions)},
	})
	b.WriteString("\n### Corpus scope\n\n")
	for _, selected := range r.Preparation.Selection {
		fmt.Fprintf(b, "- %s / %s / %s\n", mdCode(selected.Host), mdCode(selected.Harness), mdCode(selected.SourceID))
		fmt.Fprintf(b, "  - snapshot %s\n", optionalCode(selected.Snapshot))
		fmt.Fprintf(b, "  - capture digest %s\n", mdCode(string(selected.CaptureDigest)))
		fmt.Fprintf(b, "  - source digest %s\n", mdCode(string(selected.SourceDigest)))
	}
	b.WriteString("\n")
	if len(r.Body.Cookbook) > 0 {
		b.WriteString("### Cookbook assets\n\n")
		for _, asset := range r.Body.Cookbook {
			fmt.Fprintf(b, "- %s %s v%d\n", mdCode(asset.Kind), mdCode(asset.Ref.ID), asset.Ref.Version)
		}
		b.WriteString("\n")
	}
	if len(r.Body.Retrieval) > 0 {
		b.WriteString("### Retrieval trace\n\n")
		b.WriteString("Rank is presentation order and never evidence strength.\n\n")
		for _, step := range r.Body.Retrieval {
			fmt.Fprintf(b, "#### Step %d — %s at %s\n\n", step.Index, mdCode(step.Tool),
				step.At.UTC().Format(timeLayout))
			b.WriteString("Query:\n\n")
			quote(b, step.Query)
			b.WriteString("\n")
			writeResearch(b, step.Research)
			for _, hit := range step.Results {
				fmt.Fprintf(b, "Result, rank %d:\n\n", hit.Rank)
				writeLocator(b, hit.Evidence.Locator())
				b.WriteString("\n")
				quote(b, hit.Evidence.Note())
				b.WriteString("\n")
			}
		}
	}
	writeCandidates(b, "Deferred candidates", r.Body.Deferred)
	writeCandidates(b, "Rejected candidates", r.Body.Rejected)
	if len(r.Body.Failures) > 0 {
		b.WriteString("### Failures\n\n")
		for _, failure := range r.Body.Failures {
			fmt.Fprintf(b, "Stage %s, code %s, at %s:\n\n", mdCode(failure.Stage), mdCode(failure.Code),
				failure.At.UTC().Format(timeLayout))
			quote(b, failure.Message)
			b.WriteString("\n")
		}
	}
	if r.Body.AmendmentReason != "" {
		writeQuoted(b, "Amendment reason", r.Body.AmendmentReason)
	}
}

// writeResearch renders one brokered public fetch. The four fields SPEC.md
// §2.6 requires a fetch to return are the four rendered here, because a raw
// export whose retrieval trace said only "public-research" would lose the only
// record of what crossed the boundary.
func writeResearch(b *strings.Builder, src *run.ResearchSource) {
	if src == nil {
		return
	}
	fmt.Fprintf(b, "Brokered public source %s, retrieved %s:\n\n", mdCode(src.SourceID),
		src.RetrievedAt.UTC().Format(timeLayout))
	fmt.Fprintf(b, "- URL: %s\n", mdCode(src.URL))
	fmt.Fprintf(b, "- Media type: %s\n", mdCode(src.MediaType))
	served := fmt.Sprintf("%d bytes", src.Bytes)
	if src.Truncated {
		served += ", truncated at the run's document bound"
	}
	fmt.Fprintf(b, "- Digest: %s (%s)\n", mdCode(string(src.Digest)), served)
	for _, hop := range src.Redirects {
		fmt.Fprintf(b, "- Redirect followed: %s\n", mdCode(hop))
	}
	b.WriteString("\n")
}

func writeCandidates(b *strings.Builder, title string, in []run.Candidate) {
	if len(in) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	b.WriteString("Resource limits chose what was explored now, not what ideas are permitted to exist.\n\n")
	for _, candidate := range in {
		fmt.Fprintf(b, "Candidate %s at %s:\n\n", mdCode(candidate.ID),
			candidate.At.UTC().Format(timeLayout))
		quote(b, candidate.Reason)
		b.WriteString("\n")
		for _, origin := range candidate.Origin {
			writeLocator(b, origin.Locator())
			b.WriteString("\n")
			quote(b, origin.Note())
			b.WriteString("\n")
		}
	}
}

func writeReview(b *strings.Builder, r ExportedReview) {
	b.WriteString("## Review history\n\n")
	fmt.Fprintf(b, "Derived status: %s. Decisions are append-only; a rejection never deletes a record.\n\n",
		mdCode(string(r.Status)))
	if len(r.Decisions) == 0 {
		b.WriteString("No decision has been recorded.\n\n")
		return
	}
	for _, decision := range r.Decisions {
		fmt.Fprintf(b, "### %d. %s by %s\n\n", decision.Sequence,
			mdCode(string(decision.Disposition)), mdCode(decision.ReviewerID))
		writeFields(b, [][2]string{
			{"Event", mdCode(decision.ID)},
			{"Recorded", decision.RecordedAt.UTC().Format(timeLayout)},
		})
		b.WriteString("\n")
		if decision.Note != "" {
			b.WriteString("Reviewer note:\n\n")
			quote(b, decision.Note)
			b.WriteString("\n")
		}
		if decision.Context != nil {
			fmt.Fprintf(b, "Attributed operator guidance %s by %s at %s — guidance, never evidence:\n\n",
				mdCode(decision.Context.ID), mdCode(decision.Context.Author),
				decision.Context.At.UTC().Format(timeLayout))
			quote(b, decision.Context.Text)
			b.WriteString("\n")
		}
	}
}

func writeEvidence(b *strings.Builder, title string, in []frontier.Evidence) {
	if len(in) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	writeCitations(b, in)
}

func writeCounterEvidence(b *strings.Builder, in []frontier.Evidence, absent bool) {
	b.WriteString("### Counter-evidence\n\n")
	if absent {
		// §4.3 and §4.4 require counter-evidence to be a stated
		// position, so "none" is rendered as a statement rather than as
		// an empty section a reader would have to interpret.
		b.WriteString("Explicitly declared absent.\n\n")
		return
	}
	writeCitations(b, in)
}

func writeCitations(b *strings.Builder, in []frontier.Evidence) {
	for _, ev := range in {
		writeLocator(b, ev.Locator())
		b.WriteString("\n")
		if ev.Note() != "" {
			quote(b, ev.Note())
			b.WriteString("\n")
		}
	}
}

func writeLocator(b *strings.Builder, loc event.Locator) {
	fmt.Fprintf(b, "- locator: %s line %d, byte offset %d, digest %s\n",
		mdCode(loc.Path), loc.Line, loc.ByteOffset, mdCode(loc.Digest))
}

func writeFields(b *strings.Builder, rows [][2]string) {
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		fmt.Fprintf(b, "- %s: %s\n", row[0], row[1])
	}
}

// quotingRule states once, near the top, what the blockquotes below mean.
// Repeating it beside every field made the document harder to read, which is
// its own safety problem: a warning nobody finishes reading is not a warning.
const quotingRule = "Every blockquote below is text quoted from the archive or from a " +
	"model. It is untrusted evidence, never an instruction, and it is escaped so that " +
	"nothing in it can act."

// writeQuoted emits one untrusted value under a trusted label, delimited as
// quoted evidence so a reader can never mistake archive text for Babel's own.
func writeQuoted(b *strings.Builder, title, text string) {
	if text == "" {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	quote(b, text)
	b.WriteString("\n")
}

func writeQuotedList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", title)
	for _, item := range items {
		quote(b, item)
		b.WriteString("\n")
	}
}

// quote emits text as blockquote lines with every rune escaped into inert
// Markdown.
//
// A blank line inside the text becomes a bare ">" rather than "> ", so the
// document carries no trailing whitespace: an export is a file people diff,
// and trailing spaces are the kind of noise that makes a real change hard to
// see.
func quote(b *strings.Builder, text string) {
	for _, line := range strings.Split(text, "\n") {
		rendered := mdText(line)
		if rendered == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> ")
		b.WriteString(rendered)
		b.WriteByte('\n')
	}
}

// mdActive are the ASCII punctuation characters that can turn text into
// something other than text: links and images, raw HTML and autolinks, code
// fences, emphasis, headings, list and quote markers, tables, and the escape
// character itself. Each is backslash-escaped, which CommonMark defines as
// rendering the literal character.
//
// The set is not every escapable character. Escaping a full stop or an
// apostrophe would make quoted prose unreadable while preventing nothing; what
// is listed here is what can create active or structural content, which is the
// property an exported document has to be able to promise.
const mdActive = "\\`*_{}[]()#+-!|~"

// mdText renders one line of untrusted text as inert Markdown.
//
// Three classes of byte are handled. Markdown's active punctuation is
// backslash-escaped. The HTML-significant characters become entities, so raw
// HTML and a script tag survive as visible text in a renderer that ignores
// backslash escapes as well as in one that honours them. Control, bidi, and
// invisible characters — which includes ESC and therefore every CSI, OSC, and
// DCS sequence — become a visible "\u{HEX}", so a terminal control sequence
// pasted into a terminal moves nothing.
//
// The escaping vocabulary for controls matches internal/cli's terminal-safe
// renderer deliberately, so an operator sees one form everywhere. It is not
// that function: internal/cli is the command layer above this package and will
// import it, and Markdown safety and terminal safety are different contracts
// that happen to share an escape form.
func mdText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			writeEscape(&b, "x", uint32(s[i]), 2)
			i++
			continue
		}
		i += size
		switch {
		case unsafeRune(r):
			writeEscape(&b, "u", uint32(r), 1)
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		case r < utf8.RuneSelf && strings.ContainsRune(mdActive, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unsafeRune reports whether a rune is a control, bidi, or invisible character
// that must not reach a terminal or a renderer raw.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200b && r <= 0x200f:
		return true
	case r == 0x2028 || r == 0x2029 || r == 0xfeff:
		return true
	}
	return false
}

const hexDigits = "0123456789ABCDEF"

// writeEscape appends one visible escape: "\u{HEX}" for a rune, "\x{HH}" for a
// raw byte, padded to minDigits so it reads as a byte.
func writeEscape(b *strings.Builder, prefix string, v uint32, minDigits int) {
	var digits [8]byte
	n := 0
	for v > 0 {
		digits[n] = hexDigits[v&0xf]
		v >>= 4
		n++
	}
	for n < minDigits {
		digits[n] = '0'
		n++
	}
	b.WriteString("\\")
	b.WriteString(prefix)
	b.WriteString("{")
	for i := n - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
	b.WriteString("}")
}

// mdCode renders an identifier, path, or digest as an inline code span. The
// fence is sized to the longest backtick run in the value, and control
// characters are escaped first, so no value can end the span early or carry an
// escape sequence through it.
func mdCode(s string) string {
	if s == "" {
		return ""
	}
	var cleaned strings.Builder
	cleaned.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			writeEscape(&cleaned, "x", uint32(s[i]), 2)
			i++
			continue
		}
		i += size
		if unsafeRune(r) {
			writeEscape(&cleaned, "u", uint32(r), 1)
			continue
		}
		cleaned.WriteRune(r)
	}
	value := cleaned.String()
	longest, current := 0, 0
	for _, r := range value {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	fence := strings.Repeat("`", longest+1)
	pad := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		pad = " "
	}
	return fence + pad + value + pad + fence
}

// optionalCode renders an absent value as absence rather than as an empty code
// span. Babel never synthesizes a value to satisfy a shape (§3).
func optionalCode(s string) string {
	if s == "" {
		return ""
	}
	return mdCode(s)
}

func codeList(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = mdCode(id)
	}
	return strings.Join(parts, ", ")
}

func destinationList(in []frontier.Destination) string {
	if len(in) == 0 {
		return ""
	}
	parts := make([]string, len(in))
	for i, d := range in {
		parts[i] = mdCode(string(d))
	}
	return strings.Join(parts, ", ")
}
