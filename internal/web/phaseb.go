package web

// This file holds the request plumbing every Phase B route shares: paging,
// request-body decoding, operator attribution, and the classification that
// turns a service refusal into a status and a message.
//
// It is separate from the handlers because these four are the rules the whole
// Phase B surface is judged by, and a rule implemented once per route is a rule
// that eventually differs per route.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
)

const (
	// defaultPageLimit is the page a list route serves when the caller names
	// none, and maxPageLimit the largest it will serve at all. A corpus of
	// thousands of records must not travel whole: a request for more pages
	// is one more request, while a response that ignored a bound would put
	// the whole frontier in one browser's memory.
	defaultPageLimit = 50
	maxPageLimit     = 200

	// listScanCap bounds how many record identifiers one list route
	// enumerates before it paginates. internal/frontier and internal/review
	// answer questions about records a caller names rather than offering a
	// paged enumeration, so a listing here assembles identifiers first; this
	// is the ceiling on that assembly, high enough to cover a real local
	// frontier and low enough that a pathological one cannot make a single
	// request unbounded.
	listScanCap = 5000

	// maxRequestBody bounds a mutation's JSON body. Every field a Phase B
	// mutation accepts is an identifier, a disposition, or a reviewer's
	// note, none of which needs a megabyte; the bound exists so a local page
	// cannot make the server hold an arbitrary amount of memory.
	maxRequestBody = 1 << 20
)

// page is one caller's requested window over a list route's result.
type page struct {
	limit  int
	offset int
}

// requirePage reads the shared ?limit=&offset= pair, refusing a page the route
// will not serve rather than silently clamping it: a client that asked for a
// thousand records and received two hundred cannot tell that it is paging
// through a truncated view.
func (s *Server) requirePage(w http.ResponseWriter, r *http.Request) (page, bool) {
	limit, ok := queryInt(r, "limit", defaultPageLimit)
	if !ok || limit <= 0 || limit > maxPageLimit {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxPageLimit))
		return page{}, false
	}
	offset, ok := queryInt(r, "offset", 0)
	if !ok || offset < 0 {
		s.writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return page{}, false
	}
	return page{limit: limit, offset: offset}, true
}

// window is the half-open slice bounds this page selects from n items. An
// offset past the end selects nothing rather than failing: a page that no
// longer exists because records were added is an empty page, not an error.
func (p page) window(n int) (int, int) {
	start := min(p.offset, n)
	return start, min(start+p.limit, n)
}

// requireID reads a required record identifier from the query string. It is the
// counterpart of the selector helper Phase A's routes share.
func (s *Server) requireID(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	value := r.URL.Query().Get(key)
	if value == "" {
		s.writeError(w, http.StatusBadRequest, key+" is required")
		return "", false
	}
	return value, true
}

// requireService refuses a route whose service is not wired. It is a 409 rather
// than a 404 or a 500 for the same reason the archive routes are: the route
// exists and the request is well formed, but this build holds no analysis state
// to answer it from.
func (s *Server) requireService(w http.ResponseWriter, wired bool, name string) bool {
	if wired {
		return true
	}
	s.writeError(w, http.StatusConflict, name+" is not available in this session")
	return false
}

// requireOperator produces the attributed identity a §4.7 or §4.8 mutation
// records as its author.
//
// The refusal is review.NewAuthority's rather than this handler's, which is
// deliberate: the rule that a decision must name someone belongs to the service
// that stores decisions, and a web surface that invented a fallback author —
// "web", "operator", the session token — would be recording that a decision was
// made without recording who made it. §4.8's acceptance takes the same identity
// for the same reason, so both mutations resolve their author here.
func (s *Server) requireOperator(w http.ResponseWriter) (review.Authority, bool) {
	by, err := review.NewAuthority(s.opts.Operator)
	if err != nil {
		s.writeError(w, http.StatusConflict,
			"this session has no operator identity, and a decision that records no author is not a decision")
		return review.Authority{}, false
	}
	return by, true
}

// decodeBody reads a mutation's JSON body.
//
// Unknown fields are refused. A mutation whose misspelled field was ignored
// would report success for a decision that lost its note or its duplicate
// target, and an append-only log cannot be corrected afterwards.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		s.writeError(w, http.StatusBadRequest, "request body is not the JSON object this route accepts")
		return false
	}
	return true
}

// serviceError reports a service refusal.
//
// Nothing from the error's own text reaches the client, and nothing reaches the
// diagnostics stream either. A wrapped service error can carry a database path,
// a connection string, an evidence locator, or corpus text, and §9 keeps
// credentials out of logs and errors; the rule that refused is what a caller
// can act on, so that is all either side receives.
func (s *Server) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := classifyService(err)
	s.logf("%s %s refused: %s", r.Method, r.URL.Path, message)
	s.writeError(w, status, message)
}

// classifyService maps the sentinel errors the Phase B services document onto a
// status and a fixed message. An unrecognized error is one status and one
// sentence: an error nobody classified is an error nobody has read, and echoing
// it would publish whatever it happens to quote.
func classifyService(err error) (int, string) {
	if status, message, ok := classifyRecordAction(err); ok {
		return status, message
	}
	switch {
	case errors.Is(err, review.ErrUnknownRecord),
		errors.Is(err, reality.ErrUnknownRecord),
		errors.Is(err, frontier.ErrUnknownEntity),
		errors.Is(err, run.ErrNotFound):
		return http.StatusNotFound, "no record with that identifier"
	case errors.Is(err, review.ErrNotReviewable), errors.Is(err, frontier.ErrNotReviewable):
		return http.StatusBadRequest, "this record kind carries no review decision"
	case errors.Is(err, review.ErrTerminalStatus):
		return http.StatusConflict, "this record's review status accepts no further decision"
	case errors.Is(err, review.ErrNoChange):
		return http.StatusConflict, "this decision repeats the record's standing decision"
	case errors.Is(err, reality.ErrAlreadyDecided):
		return http.StatusConflict, "this plan has already been accepted or rejected"
	case errors.Is(err, reality.ErrInvalidTransition):
		return http.StatusConflict, "the question's state does not accept this answer"
	case errors.Is(err, reality.ErrNotAuthoritative):
		return http.StatusForbidden, "this authority cannot make a fact authoritative"
	case errors.Is(err, reality.ErrCredentialMaterial):
		return http.StatusBadRequest, "credential-shaped material is refused"
	case errors.Is(err, reality.ErrNoHypothesisSink):
		return http.StatusConflict, "this plan cannot be applied without a frontier to retain its candidate"
	case errors.Is(err, reality.ErrConflict), errors.Is(err, reality.ErrNotReversible):
		return http.StatusConflict, "this record has already been superseded or reversed"
	case errors.Is(err, review.ErrInvalidValue),
		errors.Is(err, reality.ErrInvalidValue),
		errors.Is(err, frontier.ErrInvalidValue):
		return http.StatusBadRequest, "a value in the request is outside what this record accepts"
	case errors.Is(err, index.ErrMatchTooLong):
		return http.StatusBadRequest, "the search expression is too long"
	case errors.Is(err, index.ErrNoSearchableTerm):
		return http.StatusBadRequest, "the search expression contains no searchable term"
	case errors.Is(err, index.ErrLimit), errors.Is(err, index.ErrOffset),
		errors.Is(err, index.ErrOrder), errors.Is(err, index.ErrRelevanceWithoutMatch):
		return http.StatusBadRequest, "the search request is outside what retrieval accepts"
	default:
		return http.StatusInternalServerError, "the request could not be completed"
	}
}

// refKind resolves a record kind a caller named. The vocabulary is
// internal/frontier's rather than a restatement of it, so the two cannot drift.
func refKind(value string) (frontier.EntityType, bool) {
	switch frontier.EntityType(value) {
	case frontier.EntityHypothesis:
		return frontier.EntityHypothesis, true
	case frontier.EntityObservation:
		return frontier.EntityObservation, true
	case frontier.EntityFinding:
		return frontier.EntityFinding, true
	case frontier.EntityProposal:
		return frontier.EntityProposal, true
	}
	return "", false
}

// refView is how a record reference appears on the wire, in both directions:
// a decision names its subject this way and a lineage edge reports one.
type refView struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func viewRef(ref frontier.Ref) refView {
	return refView{Type: string(ref.Type), ID: ref.ID}
}

// timeText renders a timestamp the way Phase A's surfaces already do — UTC
// RFC3339, and the empty string for a zero value so an absent time stays
// absent. An absent timestamp is a real state here: a queue row nobody has
// decided has no decision time, and printing year one would invent one.
func timeText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
