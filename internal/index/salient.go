package index

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// The bounds on one indexed term. They exist so that a body of transcript text
// yields words rather than identifiers.
//
// MinTermRunes drops the articles, operators and fragments that carry no
// subject at all. MaxTermRunes drops the other end: a 64-character digest, a
// base64 blob and a stack frame are not terms a later query will ever be
// typed with, and they are exactly what an unbounded tokenizer collects most
// of from a corpus of agent transcripts.
const (
	MinTermRunes = 4
	MaxTermRunes = 24
)

// DefaultSalientTerms is how many terms one scope contributes to a frontier
// query by default.
//
// It is sized against the query it feeds rather than chosen round.
// MaxMatchTerms is 32, and buildMatch answers a longer expression on its
// first 32 terms, so anything past that is silently discarded — while
// MaxMatchBytes bounds the whole expression at a kilobyte. Twenty-four terms
// of ordinary corpus vocabulary is about two hundred bytes and leaves the
// caller room to add its own, which is what keeps a bounded query honest: the
// terms that travel are the terms that were chosen.
const DefaultSalientTerms = 24

// maxDistinctTerms bounds one Salience's memory.
//
// The corpus is measured in gigabytes and its vocabulary grows with it — mostly
// in identifiers, which the term rules above already refuse most of. This is
// the backstop for the rest: past the bound, occurrences of terms already
// counted keep being counted and new ones are ignored, so a preparation over a
// third-of-a-gigabyte session costs a bounded map rather than one proportional
// to the file. The terms that matter are frequent, and a frequent term appears
// long before a hundred thousand distinct ones have.
const maxDistinctTerms = 1 << 17

// Salience accumulates the salient terms of a body of corpus text: what a
// scope is about, computed mechanically and with no model involved.
//
// The measure is Luhn's, and it is chosen because the alternatives are wrong
// in ways this corpus makes obvious. Raw frequency returns "error", "file" and
// "true" from any set of agent transcripts, because those are what transcripts
// are made of. Rarity alone returns the opposite noise — a term appearing in
// one record out of thousands is a typo or an identifier. What discriminates
// is the product: a term repeated often across some records but not all of
// them is what that scope is about, and
//
//	score(t) = occurrences(t) * ln(records / records containing t)
//
// says exactly that. A term in every record scores zero however often it
// appears, which is how the boilerplate removes itself without a stopword list
// anybody has to maintain, and a term appearing in one record of a large scope
// is refused outright rather than left to be rescued by a large logarithm.
//
// Nothing here is a ranking of importance and nothing downstream may read it
// as one. The terms are a query — the mechanical answer to "what would you
// search the frontier for, given this scope" — and §5.4's rule that retrieval
// rank is never evidence strength applies to what they retrieve.
type Salience struct {
	records int
	terms   map[string]*termStat
	full    bool
}

// termStat is one term's two counts: how often it occurs, and in how many
// records. Both are needed and neither substitutes for the other.
type termStat struct {
	occurrences int
	records     int
	// lastRecord is the record ordinal this term was last seen in, so
	// records is a document frequency rather than a second occurrence
	// count. It costs one int per term and removes the per-record set
	// allocation the obvious implementation needs.
	lastRecord int
}

// NewSalience returns an empty accumulator.
func NewSalience() *Salience {
	return &Salience{terms: make(map[string]*termStat)}
}

// Add counts one record's text. A record is the unit document frequency is
// measured in, which is the same unit the retrieval index stores and the same
// unit a hit recovers — so "appears in many records" here means the same thing
// it means in a search result.
func (s *Salience) Add(text string) {
	s.records++
	forEachTerm(text, func(term string) {
		stat, ok := s.terms[term]
		if !ok {
			if s.full || len(s.terms) >= maxDistinctTerms {
				s.full = true
				return
			}
			stat = &termStat{}
			s.terms[term] = stat
		}
		stat.occurrences++
		if stat.lastRecord != s.records {
			stat.lastRecord = s.records
			stat.records++
		}
	})
}

// Records reports how many records have been counted, which a caller needs in
// order to say honestly that a query came from an empty scope rather than from
// a scope with nothing to say.
func (s *Salience) Records() int { return s.records }

// Terms reports the highest scoring terms, most salient first, at most limit
// of them. A limit of zero means DefaultSalientTerms.
//
// The order is total: ties break on the term itself, so the same corpus
// produces the same query on every machine and a preparation that records one
// is reproducible rather than approximately reproducible.
func (s *Salience) Terms(limit int) []string {
	if limit <= 0 {
		limit = DefaultSalientTerms
	}
	if s.records == 0 {
		return nil
	}
	// A term appearing in exactly one record is refused once the scope is
	// large enough for that to mean something. Below three records it means
	// nothing at all — every term of a two-record scope is in one or both —
	// so the rule would empty the query instead of cleaning it.
	minRecords := 1
	if s.records > 2 {
		minRecords = 2
	}
	type scored struct {
		term  string
		score float64
	}
	ranked := make([]scored, 0, len(s.terms))
	total := float64(s.records)
	for term, stat := range s.terms {
		if stat.records < minRecords {
			continue
		}
		score := float64(stat.occurrences) * math.Log(total/float64(stat.records))
		if score <= 0 {
			// A term in every record. It says nothing about this scope
			// that it does not also say about every other one.
			continue
		}
		ranked = append(ranked, scored{term: term, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].term < ranked[j].term
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.term)
	}
	return out
}

// Query renders the terms as a match expression for Search and
// FrontierSearch. The terms are space separated because that is buildMatch's
// grammar for an optional-term query: membership is broad, bm25 decides the
// order, and Limit bounds what a caller is handed.
func (s *Salience) Query(limit int) string { return strings.Join(s.Terms(limit), " ") }

// TermOverlap reports how much of the shorter of two texts' vocabulary the
// longer one also holds, between 0 and 1.
//
// The denominator is the smaller term set on purpose. A near-duplicate is
// usually not a rewording of equal length: it is the same idea stated again,
// shorter or longer, and a symmetric measure like Jaccard scores "the release
// pipeline skips its own tests" against a three-paragraph restatement of it as
// barely related. Containment answers the question actually being asked —
// does one of these say what the other already said — and it is why the
// measure is deliberately not symmetric in the lengths it compares.
//
// It is a heuristic and it is named as one everywhere it is used. Two texts
// sharing their vocabulary may state opposite things, so nothing may act on
// this number: it warns a reader, and honesty about a possible duplicate is
// worth more than the tidiness of dropping one.
func TermOverlap(a, b string) float64 {
	left, right := termSet(a), termSet(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	if len(right) < len(left) {
		left, right = right, left
	}
	shared := 0
	for term := range left {
		if _, ok := right[term]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(left))
}

// termSet is one text's vocabulary for the overlap measure, with a trailing
// plural folded away.
//
// The fold exists because the measure compares two statements of the same idea
// written by different runs, and English morphology alone was enough to defeat
// it: "the pipeline skips the checks it claims to run" against "pipeline runs
// skip the checks they claim" shares three terms of six unfolded and five of
// six folded, and only the second number describes what a reader sees. It is
// applied here and deliberately not in Salience: those terms become an FTS5
// query against an index tokenized by unicode61, which does no stemming, so a
// folded query term would match nothing.
//
// The length guard is what keeps it from being a stemmer. Folding is refused
// below five runes, so "bus", "less" and "runs" are left alone, and no attempt
// is made at anything harder — an irregular plural or a tense change is a miss,
// and a miss costs one absent warning rather than a wrong one.
func termSet(text string) map[string]struct{} {
	set := make(map[string]struct{})
	forEachTerm(text, func(term string) {
		runes := len([]rune(term))
		if runes >= 5 && strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss") {
			term = strings.TrimSuffix(term, "s")
		}
		set[term] = struct{}{}
	})
	return set
}

// forEachTerm calls fn for every indexable term in text.
//
// The token boundary is unicode61's — letters and digits are token characters
// and everything else separates — so a term counted here is a term the FTS5
// index can actually be searched for. Case folding is the tokenizer's too.
//
// The one rule that is this function's own is the digit rule: a term more than
// half of whose runes are digits is an identifier, not a word. Session ids,
// hex fragments, timestamps and version strings are the bulk of what a
// transcript corpus contributes to a vocabulary, they are unsearchable as
// subject matter, and they are indistinguishable from words by length alone.
func forEachTerm(text string, fn func(string)) {
	var b strings.Builder
	digits := 0
	runes := 0
	flush := func() {
		if runes >= MinTermRunes && runes <= MaxTermRunes && digits*2 <= runes {
			fn(b.String())
		}
		b.Reset()
		digits = 0
		runes = 0
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
			runes++
		case unicode.IsDigit(r):
			b.WriteRune(r)
			runes++
			digits++
		default:
			flush()
		}
		if runes > MaxTermRunes {
			// Past the bound the term is already refused, so the rest of
			// its runes are dropped rather than buffered: an 8 MiB base64
			// blob with no separator in it is one token, and buffering it
			// would make one record's memory the record's size.
			b.Reset()
			runes = MaxTermRunes + 1
		}
	}
	flush()
}
