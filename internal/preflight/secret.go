package preflight

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

// A detector is one rule. Structural detectors match a documented credential
// format; the single heuristic detector guesses from shape alone. The split is
// visible in every finding, because a reviewer treats "this is an AWS access
// key id" and "this looks random" differently and a report that calls both
// "detected" has thrown that difference away.
type detector struct {
	name       string
	confidence Confidence
	summary    string

	// re matches the credential in context. value names the submatches that
	// hold the credential itself, first non-empty winning; the surrounding
	// context — a field name, an Authorization prefix, a URL's host — stays
	// out of the redacted range so redacted text remains readable.
	re    *regexp.Regexp
	value []int

	// reject drops a match the pattern is too coarse to exclude, such as an
	// obvious placeholder or a path to a credential file rather than one.
	reject func(value string) bool
}

// detectors is the rule table, in the order it is documented. Every entry is
// anchored on something a credential format documents about itself: an armour
// header, a vendor prefix with a fixed length, a URL's userinfo grammar, a
// field name beside a literal. The last entry is the only guess.
var detectors = []detector{
	{
		name:       "aws-access-key-id",
		confidence: ConfidenceStructural,
		summary:    "an AWS access key id: a documented key-type prefix followed by the exact identifier length",
		re:         regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA|AIPA|ANPA|AROA|A3T[A-Z0-9])[0-9A-Z]{16}\b`),
	},
	{
		name:       "google-api-key",
		confidence: ConfidenceStructural,
		summary:    "a Google API key: a documented prefix followed by the exact key length",
		re:         regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
	},
	{
		name:       "vendor-token",
		confidence: ConfidenceStructural,
		summary:    "a vendor-issued token carrying its issuer's documented prefix and minimum length",
		re: regexp.MustCompile(`\b(?:gh[pousr]_[0-9A-Za-z]{20,}` +
			`|github_pat_[0-9A-Za-z_]{20,}` +
			`|glpat-[0-9A-Za-z_\-]{16,}` +
			`|xox[abprs]-[0-9A-Za-z\-]{10,}` +
			`|sk-[0-9A-Za-z_\-]{20,}` +
			`|(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,})`),
	},
	{
		name:       "jwt",
		confidence: ConfidenceStructural,
		summary:    "a JSON Web Token: a base64url header that decodes to the start of a JSON object, then a payload and a signature",
		// "eyJ" is base64url for the first two bytes of `{"`, so the header
		// segment declares itself. Three dot-separated base64url segments
		// without it are just three words.
		re: regexp.MustCompile(`\beyJ[0-9A-Za-z_\-]{10,}\.[0-9A-Za-z_\-]{10,}\.[0-9A-Za-z_\-]{10,}`),
	},
	{
		name:       "bearer-token",
		confidence: ConfidenceStructural,
		summary:    "a bearer credential presented the way an Authorization header presents one",
		re:         regexp.MustCompile(`(?i)\bbearer\s+([0-9A-Za-z\-._~+/]{20,}={0,2})`),
		value:      []int{1},
		reject:     rejectNonCredentialValue,
	},
	{
		name:       "credential-assignment",
		confidence: ConfidenceStructural,
		summary:    "a credential-named field assigned a literal value",
		// The field name is the structure here: a name whose stem is a
		// credential word, a separator, and a literal long enough to be a
		// credential. The name itself is deliberately left outside the
		// redacted range so a reviewer can still see what was set.
		re: regexp.MustCompile(`(?i)\b[a-z0-9_.\-]{0,32}` +
			`(?:api[_\-]?key|apikey|access[_\-]?key|secret|password|passwd|pwd|token|credential|auth[_\-]?token|bearer)` +
			`[a-z0-9_.\-]{0,16}["']?\s*(?::=|=>|::|:|=)\s*` +
			`(?:"([^"\n]{8,256})"|'([^'\n]{8,256})'|([^\s"',;)\]}]{8,256}))`),
		value:  []int{1, 2, 3},
		reject: rejectNonCredentialValue,
	},
	{
		name:       "connection-string",
		confidence: ConfidenceStructural,
		summary:    "a URL with a password embedded in its userinfo, which travels wherever the URL is copied",
		// Only the password is redacted: the scheme, user, and host are the
		// context that makes the finding actionable, and none of them is the
		// credential.
		re:     regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.\-]{1,20}://[^\s:/?#@\[\]]{0,64}:([^\s:/?#@]{1,128})@[^\s/?#]{1,255}`),
		value:  []int{1},
		reject: rejectNonCredentialValue,
	},
	{
		name:       "private-key-block",
		confidence: ConfidenceStructural,
		summary:    "a PEM private-key armour block, which is a credential in its entirety",
		// Matched by armourMatches rather than by re: a private key is
		// larger than one record, so both the record that opens the block and
		// the record that closes it must be recognized on their own.
	},
	{
		name:       "high-entropy-string",
		confidence: ConfidenceHeuristic,
		summary:    "an unstructured token long, mixed, and dense enough to look like a credential; a guess about shape, not a match against a format",
		// Matched by entropyMatches rather than by re.
	},
}

// Detector names, used where a check refers to a specific rule.
const (
	detectorPrivateKey = "private-key-block"
	detectorEntropy    = "high-entropy-string"
)

func detectorByName(name string) *detector {
	for i := range detectors {
		if detectors[i].name == name {
			return &detectors[i]
		}
	}
	panic("preflight: unknown detector " + name)
}

var (
	privateKeyDetector = detectorByName(detectorPrivateKey)
	entropyDetector    = detectorByName(detectorEntropy)
)

// match is one accepted detection: the detector that fired and the byte range
// of the credential itself, which is the range redaction replaces.
type match struct {
	det   *detector
	start int
	end   int
}

// findSecrets returns the accepted, non-overlapping matches in text, ordered
// by position. It is the single implementation behind both the secret findings
// and Redact, so a report can never disagree with the text a hosted run sees.
func findSecrets(text string, th Thresholds) []match {
	if text == "" {
		return nil
	}
	var found []match
	for i := range detectors {
		d := &detectors[i]
		if d.re != nil {
			found = appendRegexpMatches(found, d, text)
		}
	}
	found = armourMatches(found, text)
	found = entropyMatches(found, text, th)
	found = dropPlaceholderMatches(found, text)
	return resolve(found, text)
}

// dropPlaceholderMatches discards any match touching an already-substituted
// placeholder, for every detector rather than only the heuristic.
//
// The distinction matters and it is the opposite of the rule for payload and
// armour masks. Those regions hold attacker-controlled bytes, so a structural
// detector is trusted inside them: a credential must not be able to hide by
// sitting next to an embedded image. A placeholder is not attacker-controlled
// — Babel wrote it — so nothing can hide there, and leaving detectors free to
// match it costs idempotence. A credential-shaped field name followed by a
// placeholder is exactly that: `token=[[babel-redacted:…` reads as an
// assignment whose value is a long literal, so a second pass re-redacts the
// placeholder under a new digest, corrupts the surrounding brackets, and
// destroys the recurrence signal that shared placeholders exist to carry.
//
// Overlap rather than containment is the test, because a detector's captured
// value can begin inside a placeholder and end outside it.
func dropPlaceholderMatches(in []match, text string) []match {
	spans := placeholderPattern.FindAllStringIndex(text, -1)
	if len(spans) == 0 {
		return in
	}
	out := in[:0]
	for _, m := range in {
		overlaps := false
		for _, s := range spans {
			if m.start < s[1] && s[0] < m.end {
				overlaps = true
				break
			}
		}
		if !overlaps {
			out = append(out, m)
		}
	}
	return out
}

// appendRegexpMatches applies one pattern-driven detector.
func appendRegexpMatches(out []match, d *detector, text string) []match {
	for _, idx := range d.re.FindAllStringSubmatchIndex(text, -1) {
		start, end := valueRange(d, idx)
		if start < 0 || end <= start {
			continue
		}
		if d.reject != nil && d.reject(text[start:end]) {
			continue
		}
		out = append(out, match{det: d, start: start, end: end})
	}
	return out
}

// valueRange picks the credential's own byte range out of a submatch index:
// the first non-empty named value group, or the whole match when the detector
// declares none.
func valueRange(d *detector, idx []int) (int, int) {
	for _, g := range d.value {
		if 2*g+1 < len(idx) && idx[2*g] >= 0 && idx[2*g+1] > idx[2*g] {
			return idx[2*g], idx[2*g+1]
		}
	}
	if len(d.value) > 0 {
		return -1, -1
	}
	return idx[0], idx[1]
}

var (
	// armourBegin and armourEnd match PEM armour lines. The label is captured
	// so a public key or a certificate is told apart from a private key by
	// what the armour says about itself.
	armourBegin = regexp.MustCompile(`-----BEGIN ([A-Z0-9 ]{0,48})-----`)
	armourEnd   = regexp.MustCompile(`-----END ([A-Z0-9 ]{0,48})-----`)
)

// armourMatches recognizes a private key from either end of it.
//
// This is the detector the corpus's own shape forces. A session log is JSONL,
// a private key is thousands of bytes, and a capture is crash-consistent per
// file (§6.1), so a pasted key routinely spans more than one record: one
// record opens the armour and a later one closes it. A rule that required
// both markers in one text would find neither half.
//
// So a BEGIN marker claims everything after it — to the matching END marker
// if this text has one, otherwise to the end of the text — and an END marker
// with no BEGIN before it claims everything before it. The second rule
// over-redacts a record that merely quotes an END marker in prose, and that is
// the right way to be wrong: the alternative leaks the tail of every key that
// crosses a record boundary.
func armourMatches(out []match, text string) []match {
	if !strings.Contains(text, "-----") {
		return out
	}
	begins := armourBegin.FindAllStringSubmatchIndex(text, -1)
	ends := armourEnd.FindAllStringSubmatchIndex(text, -1)

	firstBegin := len(text)
	for _, b := range begins {
		if !isPrivateLabel(text, b) {
			continue
		}
		if b[0] < firstBegin {
			firstBegin = b[0]
		}
		end := len(text)
		for _, e := range ends {
			if e[0] >= b[1] {
				end = e[1]
				break
			}
		}
		out = append(out, match{det: privateKeyDetector, start: b[0], end: end})
	}
	for _, e := range ends {
		if !isPrivateLabel(text, e) || e[0] > firstBegin {
			continue
		}
		out = append(out, match{det: privateKeyDetector, start: 0, end: e[1]})
	}
	return out
}

// isPrivateLabel reports whether an armour line's label names a private key.
// PEM armour states its own content type, so a PUBLIC KEY or CERTIFICATE block
// is public material by declaration rather than by guess.
func isPrivateLabel(text string, idx []int) bool {
	return strings.Contains(text[idx[2]:idx[3]], "PRIVATE")
}

// entropyMatches is the unstructured-secret heuristic: the rule for
// credentials with no documented shape at all.
//
// A candidate is a run of credential alphabet characters. It fires when the
// run is long, uses at least three of the four character classes, and carries
// enough Shannon entropy per character to be denser than prose or an
// identifier in the same alphabet. Padding characters are deliberately outside
// the alphabet, so `name=value` is two candidates rather than one — a field
// name merged into its value would defeat both the length and the class test.
func entropyMatches(out []match, text string, th Thresholds) []match {
	masks := maskedSpans(text)
	for i := 0; i < len(text); {
		if !isCandidateByte(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isCandidateByte(text[j]) {
			j++
		}
		if qualifies(text[i:j], th) && !overlapsAny(masks, i, j) {
			out = append(out, match{det: entropyDetector, start: i, end: j})
		}
		i = j
	}
	return out
}

// isCandidateByte reports whether b belongs to the credential alphabet the
// heuristic scans: base64, base64url, and hex all live inside it.
func isCandidateByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '+' || b == '/' || b == '_' || b == '-':
		return true
	}
	return false
}

// qualifies decides one candidate. The rejections are the whole value of the
// rule: a detector that fires on every digest, identifier, and path in a
// transcript is a detector an operator turns off.
func qualifies(run string, th Thresholds) bool {
	if len(run) < th.EntropyMinLength {
		return false
	}
	// A digest, a Git commit, a UUID: high-entropy by construction and
	// public by purpose. Content addressing means the corpus is full of them.
	if isHex(run) || uuidPattern.MatchString(run) {
		return false
	}
	if rejectNonCredentialValue(run) {
		return false
	}
	if strings.ContainsRune(run, '/') {
		// A path is short segments joined by slashes; a base64 payload is
		// long ones. Requiring one qualifying segment keeps long paths quiet
		// while still redacting the whole run when a segment does qualify,
		// so a matched blob is removed contiguously.
		for _, seg := range strings.Split(run, "/") {
			if dense(seg, th) {
				return true
			}
		}
		return false
	}
	return dense(run, th)
}

// dense applies the length, class, and entropy floors.
func dense(s string, th Thresholds) bool {
	return len(s) >= th.EntropyMinLength && classes(s) >= 3 && shannonBits(s) >= th.EntropyMinBits
}

// classes counts how many of lower, upper, digit, and symbol appear. Prose,
// snake_case identifiers, and lowercase hex reach two; random base64 reaches
// three or four. This one test removes most of what an entropy floor alone
// would report.
func classes(s string) int {
	var lower, upper, digit, other bool
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, present := range [4]bool{lower, upper, digit, other} {
		if present {
			n++
		}
	}
	return n
}

// shannonBits is the Shannon entropy of s's own byte distribution, in bits
// per character. It is computed over the candidate rather than over a corpus
// model so the rule stays local and deterministic.
func shannonBits(s string) float64 {
	var freq [256]int
	for i := range len(s) {
		freq[s[i]]++
	}
	total := float64(len(s))
	bits := 0.0
	for _, n := range freq {
		if n == 0 {
			continue
		}
		p := float64(n) / total
		bits -= p * math.Log2(p)
	}
	return bits
}

func isHex(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// nonCredentialValue matches the values that appear where a credential
	// would: documentation placeholders, shell and template references, and
	// the words a redacted or unset field is filled with. A detector that
	// reports these teaches an operator to ignore it.
	//
	// Every word alternative demands the whole value, and the ones that may
	// carry a suffix demand a separator before it. That is deliberate and it
	// is the difference between a rejection and a hole: a rule that dropped
	// any value merely beginning with a documentation word would reject every
	// real credential that happens to start with one, and an access key id
	// beginning with "A" is not a placeholder because "a" is an article.
	nonCredentialValue = regexp.MustCompile(`(?i)^(?:` +
		`x{3,}|\*{3,}|\.{3,}|-{3,}|_{3,}` +
		`|<[^>]*>|\{\{[^}]*\}\}|\$\{?[a-z_][a-z0-9_]*\}?|%[a-z0-9_]+%` +
		`|(?:your|our|my)(?:[-_][a-z0-9_\-]*)?` +
		`|(?:example|sample|dummy|placeholder|fake|test|changeme|replaceme|insert|redacted)(?:[-_][a-z0-9_\-]*|[0-9]{0,4})?` +
		`|hidden|omitted|elided|none|null|nil|true|false|empty|unset|undefined|todo|fixme` +
		`|password|passwd|secret|token|apikey|api[-_]key|credential` +
		`)$`)
)

// rejectNonCredentialValue drops the values that are documentation rather than
// credentials. A path is included: `password_file=/run/keys/pw` names where a
// credential lives, and reporting the path as the secret would both miss the
// secret and cry wolf.
func rejectNonCredentialValue(value string) bool {
	if value == "" {
		return true
	}
	if placeholderPattern.MatchString(value) {
		return true
	}
	switch value[0] {
	case '/', '$', '%', '<', '{':
		return true
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") {
		return true
	}
	if nonCredentialValue.MatchString(value) {
		return true
	}
	// One character repeated is a mask, not a key.
	if strings.Count(value, value[:1]) == len(value) {
		return true
	}
	return false
}

// payloadRunBytes is the length at which an unbroken base64 run is an
// embedded payload rather than a credential.
//
// Two facts set it. No documented credential format reaches a kilobyte in one
// unbroken run: PEM armour wraps its body, so even a key whose record escapes
// its newlines arrives as lines of about sixty characters rather than as one
// run, and every vendor token, JWT, and access key is far shorter. And
// internal/event clips an event's text to a few thousand runes, so a floor set
// at the size of a real image would never be reached by the text this
// heuristic actually sees.
//
// The consequence is stated rather than hidden: an unwrapped private key body
// pasted as one run longer than this is masked, and only the heuristic would
// have caught it anyway.
const payloadRunBytes = 1 << 10

var (
	// dataURIPattern finds an inline payload that declares itself. Length is
	// irrelevant here: `data:...;base64,` says what follows is encoded
	// content, so a small inline image is as clearly not a credential as a
	// large one.
	dataURIPattern = regexp.MustCompile(`data:[a-zA-Z0-9!#$&^_.+\-]{0,64}/?[a-zA-Z0-9!#$&^_.+\-]{0,64};base64,[A-Za-z0-9+/=]+`)

	// publicArmourPattern finds a complete armour block. Blocks whose label
	// says PRIVATE are left alone; the rest — public keys, certificates,
	// certificate requests — are public material by declaration.
	publicArmourPattern = regexp.MustCompile(`(?s)-----BEGIN ([A-Z0-9 ]{0,48})-----.*?-----END [A-Z0-9 ]{0,48}-----`)
)

// span is a half-open byte range of text the heuristic must not judge.
type span struct{ start, end int }

// maskedSpans lists the regions where the entropy heuristic is suppressed:
// embedded payloads and public armour blocks.
//
// Masks bind the heuristic only. A structural detector matched a documented
// format and is trusted inside a payload as much as outside one; suppressing
// it there would let a credential hide by being pasted next to an image.
// Already-substituted placeholders are handled separately and bind every
// detector, because that region is Babel's own output rather than
// attacker-controlled bytes — see dropPlaceholderMatches.
func maskedSpans(text string) []span {
	var out []span
	for _, p := range []*regexp.Regexp{dataURIPattern} {
		for _, idx := range p.FindAllStringIndex(text, -1) {
			out = append(out, span{idx[0], idx[1]})
		}
	}
	out = appendPayloadRuns(out, text)
	for _, idx := range publicArmourPattern.FindAllStringSubmatchIndex(text, -1) {
		if isPrivateLabel(text, idx) {
			continue
		}
		out = append(out, span{idx[0], idx[1]})
	}
	slices.SortFunc(out, func(a, b span) int { return a.start - b.start })
	return out
}

// appendPayloadRuns finds the maximal base64 runs at or above
// payloadRunBytes. It is a scan rather than a pattern because RE2 caps a
// counted repetition at a thousand, which is below the floor this rule needs.
func appendPayloadRuns(out []span, text string) []span {
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && isBase64Byte(text[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if i-start >= payloadRunBytes {
				out = append(out, span{start, i})
			}
			start = -1
		}
	}
	return out
}

func isBase64Byte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '+' || b == '/' || b == '=':
		return true
	}
	return false
}

func overlapsAny(spans []span, start, end int) bool {
	for _, s := range spans {
		if start < s.end && s.start < end {
			return true
		}
		if s.start >= end {
			break
		}
	}
	return false
}

// resolve turns overlapping candidate matches into the accepted set.
//
// Structural matches are considered before heuristic ones regardless of
// position, so a field name merged into its value cannot let a guess displace
// a format match. Within a confidence, the earlier and then the longer match
// wins, and the detector name breaks the last tie so the result does not
// depend on the order the table happens to be in.
func resolve(found []match, text string) []match {
	slices.SortFunc(found, func(a, b match) int {
		if r := rank(a.det) - rank(b.det); r != 0 {
			return r
		}
		if a.start != b.start {
			return a.start - b.start
		}
		if a.end != b.end {
			return b.end - a.end
		}
		return strings.Compare(a.det.name, b.det.name)
	})
	accepted := make([]match, 0, len(found))
	for _, m := range found {
		if m.start < 0 || m.end > len(text) {
			continue
		}
		overlap := false
		for _, a := range accepted {
			if m.start < a.end && a.start < m.end {
				overlap = true
				break
			}
		}
		if !overlap {
			accepted = append(accepted, m)
		}
	}
	slices.SortFunc(accepted, func(a, b match) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return strings.Compare(a.det.name, b.det.name)
	})
	return accepted
}

func rank(d *detector) int {
	if d.confidence == ConfidenceStructural {
		return 0
	}
	return 1
}

// secretsIn emits the secret findings for one piece of text — an event's
// normalized text or an artifact's path — against the evidence that recovers
// it.
//
// structuralOnly drops the entropy heuristic, and metadata sets it. A path is
// a name rather than content, and the names a real tree carries are exactly
// what defeats an entropy rule: cache keys, build hashes, generated
// directories, and long mixed-case identifiers. A credential that reaches a
// filename is one with a recognizable format, so the structural rules lose
// nothing here while the heuristic would report most of a corpus's artifacts.
//
// The finding records the placeholder, the matched length, and the detector.
// It never records the value: the placeholder is what lets a reviewer follow
// the same credential through a redacted transcript, and Evidence is what lets
// them read the original bytes deliberately, from the corpus, rather than
// finding them in a report they only meant to skim.
func (c *checker) secretsIn(text string, ev Evidence, reference string, structuralOnly bool) {
	for _, m := range findSecrets(text, c.th) {
		if structuralOnly && m.det.confidence != ConfidenceStructural {
			continue
		}
		value := text[m.start:m.end]
		c.add(Finding{
			Category:    CategorySecret,
			Detector:    m.det.name,
			Confidence:  m.det.confidence,
			Summary:     m.det.summary,
			Placeholder: Placeholder(value),
			ValueBytes:  len(value),
			Reference:   reference,
			Occurrences: 1,
			Evidence:    ev,
		})
	}
}
