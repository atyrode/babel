package web

// The fleet read surface: every host's committed Phase B analysis, as this
// machine can render it (SPEC.md §4.7, §6.5, §9; issue #109 item 4).
//
// Two routes are here and the rest of the file is what the existing listings
// borrow from it, which is the shape the issue asks for: a review inbox, a
// frontier and a receipt strip that show the fleet are the same pages showing
// more rows, not four new pages. So the DTOs below carry the attribution and
// the sync state, and review.go and analysis.go embed them.
//
// Three rules hold everywhere in this file.
//
// Sync state is read, never derived. internal/fleet resolves it in one place —
// remote row, then publication journal, then "local" — and a handler that
// compared timestamps or guessed from a missing row would be a second answer to
// the question SPEC.md §9 makes observable. That is why the resolution reaches
// this package as a map and not as an algorithm.
//
// Attribution is read, never substituted. A record whose origin instance
// registered no host carries no host, and this surface says so with an empty
// `host` and an explicit `host_attributed: false` rather than filling the gap
// with the local machine. Attributing one machine's analysis to another is the
// exact failure migrations/0007 and internal/fleet's absent HostID exist to
// prevent, and a renderer is the last place it could still happen.
//
// An unconfigured fleet is an answer, not a failure. A machine in local mode
// has no shared backend and therefore no other hosts, which is a fact about the
// deployment; every route here reports it as `configured: false` with a
// well-formed empty body and HTTP 200. A 409 or a 500 would make the browser
// present a working local-mode machine as a broken one, and the honest notice
// the page then shows is only possible if the response is honest first.

import (
	"context"
	"errors"
	"net/http"

	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// FleetReader is the shared-catalog read surface the fleet views render.
//
// It is an interface rather than a *fleet.Reader for the reason every other
// Phase B dependency is: the method set is the whole authority this surface
// has. Nothing here publishes, ingests or writes — Ingest is deliberately
// absent, because reconciling this machine's retrieval index against the fleet
// is work a preparation or an operator command does, and a browser GET that
// silently rebuilt an index would make a read route the busiest writer in the
// process.
type FleetReader interface {
	LocalHost() string
	Records(ctx context.Context, filter sharedcatalog.RecordFilter) ([]fleet.Record, error)
	RecordsWithContent(ctx context.Context, filter sharedcatalog.RecordFilter) ([]fleet.Record, error)
	Hosts(ctx context.Context, filter sharedcatalog.RecordFilter) ([]sharedcatalog.RecordHost, error)
	SyncStates(ctx context.Context, journal fleet.SyncJournal, ids []string) (map[string]string, error)
}

// The real reader satisfies it, asserted here rather than at the wiring site,
// for the reason types.go gives for the others: a reader method that changed
// shape is a compile failure in this package instead of a second
// implementation growing beside it.
var _ FleetReader = (*fleet.Reader)(nil)

// fleetRecordView is one fleet record as a listing shows it.
//
// Content is one bounded summary line and nothing more. The whole record is
// readable through its own kind's route once it is local, and a fleet listing
// that shipped payloads would decrypt a page of another host's analysis in
// order to render text nobody has scrolled to yet.
type fleetRecordView struct {
	RecordID string `json:"record_id"`
	RunID    string `json:"run_id"`
	Kind     string `json:"kind"`
	// Host is the display name when the host asserted one, else its id, and
	// empty when the origin instance has no registered host at all.
	// HostAttributed is what distinguishes the third case from a host whose
	// name happens to be empty: a client must be able to render an absence as
	// an absence, and a single empty string cannot say which it is.
	Host string `json:"host"`
	// HostID is the machine's identity, as opposed to Host's label. A client
	// narrowing a merged list matches on this and never on the display name:
	// migrations/0004 makes a display name a label for reading, two machines may
	// carry the same one, and a filter that matched labels would silently merge
	// them.
	HostID         string `json:"host_id"`
	HostAttributed bool   `json:"host_attributed"`
	// LocalHost marks this machine's own record. It is a separate fact from
	// Host: a row can be attributed to a host that is not this one, and this
	// one's rows must read as its own without the reader comparing ids.
	LocalHost bool `json:"local_host"`
	// Actor is the origin instance: the instance that generated the run's id
	// and committed the record. It is always present, which is why it is not
	// paired with an "attributed" flag the way Host is.
	Actor string `json:"actor"`
	// Sync is one of internal/fleet's three states, resolved by the reader:
	// "committed", "pending-sync", or "local".
	Sync string `json:"sync"`
	// CommittedAt is when the record became globally reviewable, absent while
	// its run is still pending.
	CommittedAt string `json:"committed_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
	// Unopened says why this record has no content, empty when it has some.
	// It is carried rather than swallowed because the reasons call for
	// different responses — a key to install, a binary to update, a store to
	// check — and a row that showed only a blank summary would send an
	// operator looking in the wrong place.
	Unopened string `json:"unopened,omitempty"`
}

// fleetHostView is one machine in the host filter's vocabulary.
type fleetHostView struct {
	Host   string `json:"host"`
	HostID string `json:"host_id"`
	// Attributed is false for the group of records whose origin instances
	// registered before the host column existed. The group is offered as a
	// filter option rather than hidden: "unattributed, 12 records" tells an
	// operator to expect those machines' next registration to name them,
	// where a dropped group looks like records that do not exist.
	Attributed   bool   `json:"attributed"`
	Records      int    `json:"records"`
	Pending      int    `json:"pending"`
	NewestCommit string `json:"newest_commit,omitempty"`
}

// fleetRecordList is GET /api/fleet/records.
type fleetRecordList struct {
	Configured bool              `json:"configured"`
	Items      []fleetRecordView `json:"items"`
	// Hosts is the filter's whole vocabulary, deliberately not narrowed by the
	// request's own host filter. A chip row whose options disappeared as the
	// operator selected one would leave him no way back.
	Hosts []fleetHostView `json:"hosts"`
	// Pending counts the rows in Items that are staged rather than committed,
	// so a page can mark its own staging without re-deriving the vocabulary.
	Pending int `json:"pending"`
}

// fleetHostList is GET /api/fleet/hosts: the host filter's vocabulary on its
// own, so a page can render the chips before, and independently of, whichever
// listing it narrows.
type fleetHostList struct {
	Configured bool `json:"configured"`
	// LocalHost is this machine's host id, empty when it has none. It is here
	// so a chip can say which machine the operator is sitting at without the
	// client inferring it from a row.
	LocalHost string          `json:"local_host,omitempty"`
	Hosts     []fleetHostView `json:"hosts"`
}

// fleetMark is the fleet attribution and sync state a listing row carries,
// embedded by the Phase B listings that render fleet-wide.
//
// It is one struct rather than four copies of six fields because these are the
// vocabulary issue #109 fixes across both surfaces: the same three sync values,
// the same host-or-absence rule, the same reason string. A second copy is how a
// listing ends up calling a staged record "local" while its neighbour calls it
// pending.
type fleetMark struct {
	Host           string `json:"host"`
	HostID         string `json:"host_id"`
	HostAttributed bool   `json:"host_attributed"`
	LocalHost      bool   `json:"local_host"`
	Sync           string `json:"sync"`
	CommittedAt    string `json:"committed_at,omitempty"`
	Unopened       string `json:"unopened,omitempty"`
}

// handleFleetRecords lists the deployment's Phase B records, newest commit
// first, with the host that produced each one.
//
// Content is opened, which is what makes the summary column the record's own
// wording rather than an identifier. It costs one object fetch per row and is
// bounded by the shared page limit; a per-record failure is reported in that
// row's Unopened and never fails the page, because "eleven records, one of them
// sealed with a key you do not have" is the answer to what the fleet holds.
func (s *Server) handleFleetRecords(w http.ResponseWriter, r *http.Request) {
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	filter, ok := s.fleetFilter(w, r)
	if !ok {
		return
	}
	filter.Hosts = queryValues(r, "host")
	filter.Limit, filter.Offset = pg.limit, pg.offset

	result := fleetRecordList{Items: []fleetRecordView{}, Hosts: []fleetHostView{}}
	if s.opts.Fleet == nil {
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Configured = true
	records, err := s.opts.Fleet.RecordsWithContent(r.Context(), filter)
	switch {
	case fleet.NotConfigured(err):
		s.writeJSON(w, http.StatusOK, fleetRecordList{
			Items: []fleetRecordView{}, Hosts: []fleetHostView{},
		})
		return
	case err != nil:
		s.fleetError(w, r, err)
		return
	}
	local := s.opts.Fleet.LocalHost()
	for _, record := range records {
		view := viewFleetRecord(record, local)
		if view.Sync != sharedcatalog.SyncCommitted {
			result.Pending++
		}
		result.Items = append(result.Items, view)
	}
	// The vocabulary is read without the host narrowing and without the page,
	// so the chips a client renders beside this list are the same chips it
	// renders beside every other page of it.
	vocabulary := filter
	vocabulary.Hosts, vocabulary.Limit, vocabulary.Offset = nil, 0, 0
	hosts, err := s.opts.Fleet.Hosts(r.Context(), vocabulary)
	if err != nil {
		s.fleetError(w, r, err)
		return
	}
	for _, host := range hosts {
		result.Hosts = append(result.Hosts, viewFleetHost(host))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleFleetHosts serves the host filter's vocabulary.
//
// It takes no host parameter, which is the point: this is what a host filter
// offers, and a vocabulary narrowed by the current selection could not offer
// the other machines the operator is trying to reach.
func (s *Server) handleFleetHosts(w http.ResponseWriter, r *http.Request) {
	filter, ok := s.fleetFilter(w, r)
	if !ok {
		return
	}
	result := fleetHostList{Hosts: []fleetHostView{}}
	if s.opts.Fleet == nil {
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Configured = true
	result.LocalHost = s.opts.Fleet.LocalHost()
	hosts, err := s.opts.Fleet.Hosts(r.Context(), filter)
	switch {
	case fleet.NotConfigured(err):
		s.writeJSON(w, http.StatusOK, fleetHostList{Hosts: []fleetHostView{}})
		return
	case err != nil:
		s.fleetError(w, r, err)
		return
	}
	for _, host := range hosts {
		result.Hosts = append(result.Hosts, viewFleetHost(host))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// fleetError reports a fleet read that failed.
//
// An absent fleet never reaches here — the callers answer that with
// `configured: false` — so everything that does is a shared backend that exists
// and did not answer: an unreachable catalog, an object store that refused, a
// key document this build cannot read. Rendering that as "no shared backend is
// configured" would tell the operator to configure something he already
// configured while his catalog is down, which is the one failure the honest
// empty response must not be able to cause.
//
// The generic 500 the other Phase B routes fall back to is replaced with a
// sentence about the catalog, because that is what a caller can act on. Nothing
// from the error's own text reaches the client, for serviceError's reason: a
// wrapped catalog error can carry a connection string.
func (s *Server) fleetError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := classifyService(err)
	if status == http.StatusInternalServerError {
		status, message = http.StatusBadGateway, "the shared catalog could not be read"
	}
	s.logf("%s %s refused: %s", r.Method, r.URL.Path, message)
	s.writeError(w, status, message)
}

// fleetFilter reads the ?kind=&pending= pair both fleet routes share. Neither
// bound nor host narrowing is applied here: the two routes want different ones,
// and a helper that guessed would be the reason a vocabulary silently paged.
func (s *Server) fleetFilter(w http.ResponseWriter, r *http.Request) (sharedcatalog.RecordFilter, bool) {
	kinds, ok := s.requireKinds(w, r)
	if !ok {
		return sharedcatalog.RecordFilter{}, false
	}
	pending, ok := s.requireFlag(w, r, "pending")
	if !ok {
		return sharedcatalog.RecordFilter{}, false
	}
	return sharedcatalog.RecordFilter{Kinds: kinds, IncludePending: pending}, true
}

// requireKinds resolves a repeatable ?kind= filter, refusing an unknown value
// rather than answering it with an empty list: a misspelled kind that matched
// nothing would read as a deployment that has produced none of them.
func (s *Server) requireKinds(w http.ResponseWriter, r *http.Request) ([]sharedcatalog.RecordKind, bool) {
	values := queryValues(r, "kind")
	if len(values) == 0 {
		return nil, true
	}
	kinds := make([]sharedcatalog.RecordKind, 0, len(values))
	for _, value := range values {
		kind, ok := recordKind(value)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "kind is not a Phase B record kind")
			return nil, false
		}
		kinds = append(kinds, kind)
	}
	return kinds, true
}

// recordKind resolves one shared-catalog record kind. Every arm is that
// package's own constant rather than a string, so the vocabulary cannot drift
// from the one the catalog stores; the list is here because the catalog's own
// validator is unexported.
func recordKind(value string) (sharedcatalog.RecordKind, bool) {
	switch kind := sharedcatalog.RecordKind(value); kind {
	case sharedcatalog.KindHypothesis, sharedcatalog.KindObservation,
		sharedcatalog.KindFinding, sharedcatalog.KindProposal,
		sharedcatalog.KindLink, sharedcatalog.KindDisposition,
		sharedcatalog.KindContext, sharedcatalog.KindPreparation,
		sharedcatalog.KindReceipt:
		return kind, true
	}
	return "", false
}

// requireFlag reads a 0/1 query flag the way the archive routes already do,
// refusing anything else rather than treating it as false: a client that sent
// `pending=yes` and received committed-only output would be reading a narrower
// list than it asked for with nothing saying so.
func (s *Server) requireFlag(w http.ResponseWriter, r *http.Request, key string) (bool, bool) {
	switch r.URL.Query().Get(key) {
	case "", "0":
		return false, true
	case "1":
		return true, true
	}
	s.writeError(w, http.StatusBadRequest, key+" must be 0 or 1")
	return false, false
}

// queryValues reads a repeatable query parameter, dropping empty values. An
// empty host or kind narrows a read to nothing, so accepting one would turn a
// stray `&host=` into an empty listing that reads as an empty fleet.
func queryValues(r *http.Request, key string) []string {
	raw := r.URL.Query()[key]
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// syncNotice is the degraded marker a listing envelope carries when the shared
// catalog could not answer.
//
// It exists because the alternative was refusing the listing, and refusing is
// the worse failure: this machine's durable store is local, and a shared-catalog
// outage that stops an operator reading his own hypotheses would make the fleet
// feature a liability rather than an addition. So the rows render, their sync
// state is fleet.SyncUnknown, and this says why — which is the only way
// "unknown" is a state rather than a shrug.
//
// It also covers host attribution, because both come from the same read: a
// catalog that could not answer which records committed could not answer which
// machine produced a run either.
//
// The detail is a fixed sentence rather than the error's own text, for
// serviceError's reason: a wrapped catalog error can carry a connection string.
type syncNotice struct {
	SyncDegraded bool   `json:"sync_degraded,omitempty"`
	SyncDetail   string `json:"sync_detail,omitempty"`
}

// catalogUnreachable is what a degraded listing says. It names the catalog
// rather than the failure, because the catalog is what an operator can check.
const catalogUnreachable = "the shared catalog could not be reached, so these records' " +
	"global sync state and host attribution are not known"

// degraded is the notice a listing carries when the resolution failed.
func degradedNotice() syncNotice {
	return syncNotice{SyncDegraded: true, SyncDetail: catalogUnreachable}
}

// syncStates resolves per-record sync state for a local listing, and is the
// only way this package asks the question. The second result reports that the
// resolution failed and the states are therefore fleet.SyncUnknown.
//
// The two success branches are internal/fleet's own: with a shared catalog the
// authoritative answer is the remote row, and without one the journal is all
// there is. Neither is re-derived here, which is what keeps SPEC.md §6.5's
// visible staging one answer rather than one per route.
//
// A catalog that exists and did not answer degrades rather than failing, and
// what it degrades to is a fourth value rather than one of the other three. A
// record whose remote row could not be read is not "local" — nothing observed
// that nothing is carrying it — and rendering the absence as the third state is
// exactly the lie SPEC.md §6.5 exists to prevent. The failure reaches the
// diagnostics stream and the response's notice; neither carries the error's own
// text.
func (s *Server) syncStates(ctx context.Context, r *http.Request, ids []string) (map[string]string, bool) {
	if len(ids) == 0 {
		return map[string]string{}, false
	}
	states, err := s.resolveSyncStates(ctx, ids)
	if err == nil {
		return states, false
	}
	s.logf("%s %s degraded: %s", r.Method, r.URL.Path, catalogUnreachable)
	unknown := make(map[string]string, len(ids))
	for _, id := range ids {
		unknown[id] = fleet.SyncUnknown
	}
	return unknown, true
}

func (s *Server) resolveSyncStates(ctx context.Context, ids []string) (map[string]string, error) {
	if s.opts.Fleet != nil {
		states, err := s.opts.Fleet.SyncStates(ctx, s.opts.SyncJournal, ids)
		// A reader that turns out to hold no fleet is asking the same question
		// the local resolution answers, so it is asked that way rather than
		// degrading over a fact about the deployment.
		if !fleet.NotConfigured(err) {
			return states, err
		}
	}
	return fleet.LocalSyncStates(ctx, s.opts.SyncJournal, ids)
}

// localMark is the attribution a record this machine holds carries: its own
// host, marked as this one, with the sync state the resolution above produced.
//
// An unconfigured fleet leaves the host empty rather than naming the local
// machine, because in local mode this machine has registered no host and
// printing one would be this surface inventing an identity.
func (s *Server) localMark(sync string) fleetMark {
	mark := fleetMark{LocalHost: true, Sync: sync}
	if mark.Sync == "" {
		mark.Sync = fleet.SyncLocal
	}
	if s.opts.Fleet != nil {
		if host := s.opts.Fleet.LocalHost(); host != "" {
			mark.Host, mark.HostID, mark.HostAttributed = host, host, true
		}
	}
	return mark
}

// fleetRequested reads the ?fleet=1 opt-in the merged listings take.
//
// It is opt-in rather than default because the two lists answer different
// questions. "What is on my frontier" and "what has the deployment produced"
// are both worth asking, and a listing that silently became the second would
// make an operator's own backlog look like someone else's work.
func (s *Server) fleetRequested(w http.ResponseWriter, r *http.Request) (bool, bool) {
	wanted, ok := s.requireFlag(w, r, "fleet")
	if !ok {
		return false, false
	}
	return wanted && s.opts.Fleet != nil, true
}

// otherHosts reads the other machines' committed records of the given kinds.
//
// Only committed records cross: SPEC.md §9 makes staged output not globally
// reviewable, so another host's pending record has no business appearing in
// this machine's review inbox or frontier at all. This machine's own rows are
// dropped because the listing already holds them from the durable store, where
// they carry the review status and decision history a remote row cannot.
//
// Kinds with no searchable summary — a proposal, a link — are read and returned
// like any other. Their rows carry the host, the kind and the sync state and no
// summary line, because a proposal dropped from a review inbox for want of a
// search summary would be a record silently withheld from the reviewer whose
// inbox it is.
func (s *Server) otherHosts(ctx context.Context, limit int,
	kinds ...sharedcatalog.RecordKind) ([]fleet.Record, error) {
	records, err := s.opts.Fleet.RecordsWithContent(ctx, sharedcatalog.RecordFilter{
		Kinds: kinds,
		Limit: limit,
	})
	if fleet.NotConfigured(err) {
		// No fleet means no other hosts, which is an empty block and not a
		// failed listing.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	local := s.opts.Fleet.LocalHost()
	out := make([]fleet.Record, 0, len(records))
	for _, record := range records {
		if local != "" && record.HostID == local {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// runHosts attributes runs to the machines that produced them, keyed by run id.
// The second result reports that the catalog could not answer, which leaves
// every run unattributed.
//
// Every record of a run names one origin instance, so any one of them answers
// the question. The read is by run id rather than by host, which is what makes
// this attribution rather than assumption: it comes from the origin instance's
// own registration (migrations/0007), and a run whose instance registered
// before that column existed comes back unattributed instead of assigned to
// whichever machine happens to be rendering.
//
// The loop pages because the catalog read is record-paged while the question is
// run-paged, and it stops as soon as every run is answered. The page cap is
// what keeps a receipt strip from becoming an unbounded scan; a run left
// unanswered by a truncated read renders as unattributed, which is the honest
// outcome — this surface did not observe its host.
//
// A catalog that did not answer degrades for syncStates' reason: a receipt
// strip is this machine's own durable record of its own runs, and losing it to
// another machine's outage would be the fleet feature taking away a local page.
// The rows render unattributed and the response says the attribution is not
// known, rather than an absence that reads like a registration gap.
func (s *Server) runHosts(ctx context.Context, r *http.Request,
	runIDs []string) (map[string]fleetMark, bool) {
	out := make(map[string]fleetMark, len(runIDs))
	if s.opts.Fleet == nil || len(runIDs) == 0 {
		return out, false
	}
	local := s.opts.Fleet.LocalHost()
	const maxPages = 8
	for page := range maxPages {
		records, err := s.opts.Fleet.Records(ctx, sharedcatalog.RecordFilter{
			RunIDs:         runIDs,
			IncludePending: true,
			Limit:          sharedcatalog.MaxRecordLimit,
			Offset:         page * sharedcatalog.MaxRecordLimit,
		})
		if fleet.NotConfigured(err) {
			// No fleet means no attribution to read, which is an unattributed
			// receipt strip rather than a degraded one: nothing failed.
			return out, false
		}
		if err != nil {
			s.logf("%s %s degraded: %s", r.Method, r.URL.Path, catalogUnreachable)
			return map[string]fleetMark{}, true
		}
		for _, record := range records {
			if _, found := out[record.Record.RunID]; found {
				continue
			}
			mark, _ := markFleetRecord(record, local)
			out[record.Record.RunID] = mark
		}
		if len(records) < sharedcatalog.MaxRecordLimit || len(out) == len(runIDs) {
			break
		}
	}
	return out, false
}

// viewFleetRecord renders one fleet record for a listing.
func viewFleetRecord(record fleet.Record, localHost string) fleetRecordView {
	mark, summary := markFleetRecord(record, localHost)
	return fleetRecordView{
		RecordID:       record.Record.RecordID,
		RunID:          record.Record.RunID,
		Kind:           string(record.Record.Kind),
		Host:           mark.Host,
		HostID:         mark.HostID,
		HostAttributed: mark.HostAttributed,
		LocalHost:      mark.LocalHost,
		Actor:          record.OriginInstanceID,
		Sync:           mark.Sync,
		CommittedAt:    mark.CommittedAt,
		Summary:        summary,
		Unopened:       mark.Unopened,
	}
}

// markFleetRecord resolves one record's attribution and sync state, and the one
// bounded line of its content a listing shows.
//
// The two are returned together because the callers need both and because the
// second decides part of the first: a record that opened and could not be
// summarized has no content and must say why, so the summary derivation is
// where the row's Unopened reason is finally settled.
//
// The host is the display name, then the id, then nothing — and nothing is
// reported as nothing. LocalHost compares ids rather than names, because a
// display name is a label for reading and two machines may carry the same one.
func markFleetRecord(record fleet.Record, localHost string) (fleetMark, string) {
	mark := fleetMark{
		Sync:      record.SyncState,
		HostID:    record.HostID,
		LocalHost: localHost != "" && record.HostID == localHost,
		Unopened:  record.Unopened,
	}
	switch {
	case record.HostDisplayName != "":
		mark.Host, mark.HostAttributed = record.HostDisplayName, true
	case record.HostID != "":
		mark.Host, mark.HostAttributed = record.HostID, true
	}
	if record.CommittedAt != nil {
		mark.CommittedAt = timeText(*record.CommittedAt)
	}
	if record.Published == nil {
		return mark, ""
	}
	out, err := record.Published.Output()
	switch {
	case errors.Is(err, frontier.ErrNotSearchable):
		// A kind the retrieval surface does not hold — a proposal, a link, a
		// receipt — has no summary by construction rather than by failure, so
		// this is an absent summary and not an unopened record.
		return mark, ""
	case err != nil:
		if mark.Unopened == "" {
			mark.Unopened = summaryUnavailable
		}
		return mark, ""
	}
	return mark, out.Summary
}

// viewFleetHost renders one host of the filter's vocabulary.
func viewFleetHost(host sharedcatalog.RecordHost) fleetHostView {
	view := fleetHostView{
		HostID:  host.HostID,
		Records: host.Records,
		Pending: host.Pending,
	}
	switch {
	case host.DisplayName != "":
		view.Host, view.Attributed = host.DisplayName, true
	case host.HostID != "":
		view.Host, view.Attributed = host.HostID, true
	}
	if host.NewestCommit != nil {
		view.NewestCommit = timeText(*host.NewestCommit)
	}
	return view
}

// summaryUnavailable is the reason a listing reports when a record opened and
// then could not be summarized: a payload shape this build does not read.
//
// It is a fixed sentence rather than the error's own text, for serviceError's
// reason — a wrapped derivation error can quote the payload it failed on, and
// a record's payload is the model's words about someone's corpus.
const summaryUnavailable = "this record's content could not be summarized by this build"
