package preflight

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/atyrode/babel/internal/event"
)

// placeholderPrefix and placeholderDigits define the substituted rendering.
//
// The scheme has one job beyond hiding the value: a reader must be able to see
// that a credential recurred without seeing it. So the placeholder is a
// function of the value alone — the same bytes always produce the same
// placeholder, in any session, in any run, on any machine, and two different
// values practically never collide. A per-run counter would have been cheaper
// and would have satisfied idempotence, but it would renumber whenever the
// corpus changed and could not be compared between two reports, which is
// exactly the comparison the operator needs.
//
// What that costs is stated rather than hidden: the placeholder is a truncated
// domain-separated digest, so it is a commitment to the value. A
// high-entropy credential is not recoverable from it, and a low-entropy one —
// a dictionary password — is guessable by anyone who can enumerate candidates
// and who has the placeholder. That is a real weakening compared with a
// counter, and it is accepted because the placeholder travels with local
// evidence and redacted model input, not with public output, and because
// §4.7's review of the same credential across sessions is impossible without
// a stable identity.
const (
	placeholderPrefix = "[[babel-redacted:"
	placeholderSuffix = "]]"
	// placeholderDigits is the hex length of the truncated digest. Six bytes
	// makes an accidental collision within a corpus of this size
	// negligible while keeping the placeholder short enough to read inline.
	placeholderDigits = 12
)

// placeholderPattern recognizes an already-substituted placeholder. Redaction
// skips these regions, which is what makes redaction idempotent by
// construction rather than by the accident that no detector happens to match
// its own output.
var placeholderPattern = regexp.MustCompile(`\[\[babel-redacted:[0-9a-f]{12}\]\]`)

// Placeholder returns the stable rendering of one secret value. It is exported
// because a finding's placeholder and the placeholder in redacted text must be
// the same string, and a caller comparing the two should not have to
// reimplement the derivation.
func Placeholder(value string) string {
	h := sha256.New()
	// Domain separation keeps this digest from being usable as, or confused
	// with, a content digest of the same bytes.
	h.Write([]byte("babel/preflight/placeholder/v1"))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(value)))
	sum := h.Sum(nil)
	return placeholderPrefix + hex.EncodeToString(sum[:placeholderDigits/2]) + placeholderSuffix
}

// Redact returns the text a hosted run may see: §3's step 4, which requires
// likely secrets to be redacted before hosted inference while local evidence
// keeps its locators to the original.
//
// Three properties hold, and each is a real constraint rather than a nicety.
//
// It is idempotent. Redact(Redact(t)) == Redact(t), because a placeholder is
// recognized and never re-examined.
//
// It preserves line structure. A private-key block spans lines, and replacing
// it with a single-line placeholder would change how many lines follow it; the
// newlines a match consumed are re-emitted after the placeholder, so a
// line-addressed locator over redacted text still counts the same lines. Byte
// offsets inside a record necessarily shift — a placeholder is not the length
// of the value it replaces — which is why redacted text is never the thing a
// locator addresses. RedactEvent keeps the original locator for exactly that
// reason.
//
// It replaces the credential, not its context. A field name, an Authorization
// prefix, and a URL's host survive redaction, so redacted material stays
// readable enough to be worth sending.
func Redact(text string) string {
	return redact(text, DefaultThresholds())
}

// RedactWith is Redact under caller-chosen thresholds, so the text a hosted
// run sees and the report that accompanies it are produced by the same rules.
//
// It refuses thresholds Check would refuse instead of quietly substituting the
// defaults: text redacted under rules the caller did not choose, beside a
// report written under the rules they did, is exactly the disagreement that
// makes a preflight untrustworthy.
func RedactWith(text string, th Thresholds) (string, error) {
	if err := th.validate(); err != nil {
		return "", err
	}
	return redact(text, th), nil
}

func redact(text string, th Thresholds) string {
	matches := findSecrets(text, th)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, m := range matches {
		b.WriteString(text[prev:m.start])
		value := text[m.start:m.end]
		b.WriteString(Placeholder(value))
		// Re-emit the line structure the value occupied.
		for range strings.Count(value, "\n") {
			b.WriteByte('\n')
		}
		prev = m.end
	}
	b.WriteString(text[prev:])
	return b.String()
}

// RedactEvent returns e with its text redacted and everything else — index,
// kind, role, tool, outcome, paths, and above all its locator — untouched.
//
// That asymmetry is the point of §3's split. The redacted text is what a
// hosted worker may see; the locator still addresses the original record in
// the archive, so local evidence can recover exactly what was hidden. An
// implementation that rewrote the locator to match the redacted bytes would
// have destroyed the only path back to the evidence.
func RedactEvent(e event.Event) event.Event {
	e.Text = Redact(e.Text)
	return e
}
