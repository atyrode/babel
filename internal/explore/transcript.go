package explore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/research"
)

// RedactServedResult reduces one served tool result to what Babel's own
// session log may hold: the locators that recover the bytes, and the reason
// the content itself is not there.
//
// It is the writer-side half of the split serve() makes on the wire, and it
// exists in this package because the payload schemas are this package's. A
// transcript writer decoding a SearchResults document itself would be
// asserting a schema over evidence it does not own — the same reason
// worker.Decision carries a served payload as raw JSON — and would keep
// working, wrongly, the day a facility changed what it serves.
//
// The rule it enforces is SPEC.md §9 and retrieval.go serve states it: the
// payload carries the excerpt because a model that cannot read a record
// cannot form an observation about it, and a durable record carries
// identifiers only, because a plaintext store of archive content readable by
// anyone with access to it is exactly what §9 forbids. A session log is a
// durable record. So the excerpt stops here and the locator goes on.
//
// Nothing is recognized optimistically. A payload whose schema this build
// does not publish, or that does not decode as the shape its schema claims,
// is withheld with a reason saying so, because the alternative is a facility
// one version ahead quietly widening what Babel keeps.
func RedactServedResult(result string) ([]event.Locator, string) {
	var envelope struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal([]byte(result), &envelope) != nil {
		// Not a served payload at all. Babel's own tool answers — a
		// submission accepted, a call denied with its reason — arrive as
		// plain text (worker.textResult), and the receipt is where a
		// decision and its reason belong.
		return nil, "this result is not a served evidence payload; the run's receipt records the call and Babel's decision on it"
	}
	switch envelope.Schema {
	case SearchResultsSchema:
		var payload SearchResults
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, unrecognized(envelope.Schema)
		}
		locators := make([]event.Locator, 0, len(payload.Hits))
		for _, hit := range payload.Hits {
			locators = append(locators, hit.Locator)
		}
		return locators, fmt.Sprintf(
			"%d corpus records were served; their excerpts are not persisted (SPEC.md §9) and each locator below recovers the record's bytes from the archive",
			len(payload.Hits))

	case FrontierResultsSchema:
		var payload FrontierResults
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, unrecognized(envelope.Schema)
		}
		// A frontier hit has no locator by design: a prior output's support
		// is the observations under it, and a borrowed locator would let one
		// record be cited as if the citation were its own. Its id is its
		// address, so the ids go in the reason — and its text is withheld
		// for the same reason a corpus excerpt is, because Babel's own
		// output quotes the corpus.
		ids := make([]string, 0, len(payload.Hits))
		for _, hit := range payload.Hits {
			ids = append(ids, hit.Kind+" "+hit.ID)
		}
		if len(ids) == 0 {
			return nil, "the frontier served no prior output"
		}
		return nil, fmt.Sprintf(
			"%d prior Babel outputs were served (%s); their text quotes the corpus and is not persisted, and each id above reaches the record",
			len(ids), strings.Join(ids, ", "))

	case research.CatalogSchema:
		var payload research.Catalog
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, unrecognized(envelope.Schema)
		}
		// The catalog is the operator's own list of authorized sources and
		// discloses nothing about the corpus. It is still not persisted
		// here: it is a fact about the run's configuration, which the
		// receipt's launch record already carries, and repeating it in a
		// conversation log would be a second copy of the same authorization.
		return nil, fmt.Sprintf(
			"the run's %d authorized public sources were served; the receipt's launch record names them",
			len(payload.Sources))

	case research.DocumentSchema:
		var payload research.Document
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, unrecognized(envelope.Schema)
		}
		// A fetched document is untrusted public material, and the citation
		// that makes it checkable is the URL with the digest of exactly the
		// bytes served — the whole-object locator recordFetch keeps in the
		// receipt. Persisting the content would put a copy of the public web
		// in the operator's store; the digest is what lets a re-fetch be
		// compared against what this run read.
		locator := event.Locator{Path: payload.Source.URL, Digest: string(payload.Digest)}
		return []event.Locator{locator}, fmt.Sprintf(
			"a %d byte %s document was fetched from a public source; its content is not persisted and the locator below is the URL with the digest of the bytes served",
			payload.Bytes, payload.MediaType)

	default:
		return nil, unrecognized(envelope.Schema)
	}
}

// unrecognized is the reason a payload this build cannot read is withheld.
// It names the schema rather than quoting the payload, because the one thing
// that must not happen while explaining an unreadable result is copying it.
func unrecognized(schema string) string {
	if schema == "" {
		return "a served payload declaring no schema is withheld rather than copied into a durable record (SPEC.md §9)"
	}
	return fmt.Sprintf(
		"a served payload of schema %q is not one this build can reduce to locators, so it is withheld rather than copied into a durable record (SPEC.md §9)",
		schema)
}
