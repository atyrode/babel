// Package fleet reads the Phase B analysis every host in a deployment has
// committed, and feeds it to this machine's rebuildable retrieval index
// (SPEC.md 4.7, 6.5, 9, 14; issue #109 items 3 and 4).
//
// It exists because the read half of global durability had no home. Writing is
// internal/sharedcatalog's commit protocol and internal/sync's journal;
// browsing is internal/cli's and internal/web's; and the thing in between -
// fetch the plaintext rows, open the sealed objects, decode them, attribute
// them to a machine, and hand the result to whichever surface asked - was
// implemented nowhere and would otherwise have been implemented twice, once per
// surface, with two answers to every question about which host a record came
// from and whether it is reviewable yet.
//
// Three properties hold everywhere in this package.
//
// Decryption is local and terminal. A record's content is sealed in Cellar and
// opened here, on this machine, with this machine's keyring; nothing decrypted
// is written back to any remote store, and nothing decrypted is written to any
// durable local store either. That is SPEC.md 14's local-only-decrypted-
// indexing decision, and it is what keeps the retrieval index a cache whose
// loss costs a re-index and never data.
//
// Attribution is read, never inferred. A record's host comes from the origin
// instance's own registration (migrations/0007). When that is absent - an
// instance that last registered before the column existed - this package
// reports the absence and declines to place the record on any machine's shelf,
// because a record filed under a guessed host is worse than one filed nowhere:
// the guess is invisible and the gap is not.
//
// Failure is per-record and reported. A key this instance does not hold, a
// payload shape from a newer build, an object the store cannot return: each of
// those loses one record and must not lose the read. A fleet browse that failed
// whole because one host published something this binary cannot open would make
// every machine's output hostage to every other machine's version.
package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// SyncLocal is the state of a durable record that no remote row describes and
// no publication journal is holding.
//
// It is deliberately not SyncPending. "pending-sync" is a promise that
// something will carry the record to the shared catalog, and for a record in
// this state nothing will: either this machine runs in local mode, or the
// record was never staged. SPEC.md 9 requires staged output to be visibly
// pending; claiming a record is awaiting a sync that no process intends to
// perform is the one lie that requirement must not tell.
const SyncLocal = "local"

// SyncUnknown is the state of a record whose sync state could not be
// determined, because the authority that answers the question was unreachable.
//
// It is a fourth value rather than a fallback onto one of the other three, and
// the reason is SPEC.md 3's rule that an absent value carries a reason rather
// than being synthesized. Each of the alternatives is a specific false
// statement. "committed" would claim global durability nobody verified.
// "pending-sync" would promise a sync in progress. "local" would say nothing is
// carrying this record anywhere - which is exactly the lie SPEC.md 6.5 forbids,
// because with the catalog unreachable nothing observed that at all.
//
// A listing rendering this is degraded and must say so. What it must not do is
// refuse: the local durable store is local, and a shared-catalog outage that
// stopped an operator reading their own analysis would make the fleet feature a
// liability rather than an addition.
const SyncUnknown = "unknown"

// SyncJournal answers what this machine still owes the shared catalog.
//
// It is an interface with one method because that is all this package needs,
// and because the implementation lives in internal/sync, which this package
// must not import: the publisher depends on the read path's vocabulary, not the
// other way round. Go's structural typing makes the seam free.
//
// A nil journal means this build has no publication journal wired, and the
// resolution below says so rather than guessing on its behalf.
type SyncJournal interface {
	// SyncState reports the record's local publication state, or the empty
	// string when the journal has never seen the id.
	SyncState(ctx context.Context, entityID string) (string, error)
}

// ErrNotConfigured reports a fleet read attempted without the shared backend a
// fleet read needs. It is a distinct error because the response is
// configuration rather than retry: in local mode there is no fleet, and saying
// so is the honest answer to "show me every host's records".
var ErrNotConfigured = errors.New("fleet read requires shared mode: a catalog, an object store, and a payload keyring")

// Reader is the fleet read surface. It is safe for concurrent use to the same
// extent its injected dependencies are.
type Reader struct {
	db    *sql.DB
	store sharedcatalog.ObjectStore
	ring  *envelope.Keyring
	// deployment scopes every read. It is held rather than passed per call
	// because a caller that could vary it per call could accidentally widen
	// one read past the deployment it thought it had scoped.
	deployment string
	// localHost is this machine's host id, used only to label rows and to
	// name the local partition in a report. It never filters a read: the
	// point of a fleet read is that it includes this host's committed records
	// alongside everyone else's, and the index's own local-wins rule is what
	// keeps a record from being held twice.
	localHost string
	// owned is the catalog connection this reader opened for itself, nil when
	// the connection was injected. Close releases only what it opened; see
	// open.go.
	owned *sql.DB
}

// NewReader builds a fleet reader. Every dependency is required, and a missing
// one is ErrNotConfigured rather than a nil-pointer panic three calls later.
func NewReader(db *sql.DB, store sharedcatalog.ObjectStore, ring *envelope.Keyring,
	deploymentID, localHostID string) (*Reader, error) {
	switch {
	case db == nil:
		return nil, fmt.Errorf("%w: no catalog connection", ErrNotConfigured)
	case store == nil:
		return nil, fmt.Errorf("%w: no object store", ErrNotConfigured)
	case ring == nil:
		return nil, fmt.Errorf("%w: no payload keyring", ErrNotConfigured)
	case deploymentID == "":
		return nil, fmt.Errorf("%w: no deployment id", ErrNotConfigured)
	}
	return &Reader{
		db: db, store: store, ring: ring,
		deployment: deploymentID, localHost: localHostID,
	}, nil
}

// LocalHost reports this machine's host id, which a renderer needs in order to
// label its own rows as its own.
func (r *Reader) LocalHost() string { return r.localHost }

// Record is one fleet record: its plaintext row and attribution, plus the
// decoded content when a caller asked for it and this instance could open it.
type Record struct {
	sharedcatalog.FleetRecord

	// Published is the decoded record, nil when content was not requested or
	// could not be opened.
	Published *frontier.PublishedRecord
	// Unopened says why Published is nil, empty when it is not. It carries a
	// reason rather than a boolean because the reasons call for different
	// responses - a missing key is a key to install, a newer schema is a
	// binary to update, a store error is a store to check - and a renderer
	// that showed only "unavailable" would send an operator looking in the
	// wrong place.
	Unopened string
}

// Records lists fleet records without opening any object.
//
// Content is not fetched because most of what a listing renders is not content:
// which host, which kind, when it committed, whether it is reviewable. A page
// of fifty rows costs one query here and fifty object fetches if a caller asks
// for content, so asking is explicit.
func (r *Reader) Records(ctx context.Context, filter sharedcatalog.RecordFilter) ([]Record, error) {
	filter.DeploymentID = r.deployment
	rows, err := sharedcatalog.Records(ctx, r.db, filter)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(rows))
	for i, row := range rows {
		out[i] = Record{FleetRecord: row}
	}
	return out, nil
}

// RecordsWithContent lists fleet records and opens each one.
//
// A record that cannot be opened is returned with its Unopened reason rather
// than dropped, and the call still succeeds. That is the per-record failure
// rule: the operator asked what the fleet holds, and "eleven records, one of
// them sealed with a key you do not have" is the answer, where an error would
// be a refusal to answer at all.
func (r *Reader) RecordsWithContent(ctx context.Context, filter sharedcatalog.RecordFilter) ([]Record, error) {
	out, err := r.Records(ctx, filter)
	if err != nil {
		return nil, err
	}
	for i := range out {
		published, err := r.Open(ctx, out[i].FleetRecord)
		if err != nil {
			out[i].Unopened = err.Error()
			continue
		}
		out[i].Published = &published
	}
	return out, nil
}

// Open fetches, verifies and decrypts one record's sealed object, then decodes
// the frontier record inside it.
//
// The digest check happens inside sharedcatalog.OpenRecord, before decryption,
// so a swapped or truncated object is reported as a storage fault rather than
// surfacing as an opaque authentication failure. What this adds is the decode,
// which is where a record from a newer build announces itself.
func (r *Reader) Open(ctx context.Context, rec sharedcatalog.FleetRecord) (frontier.PublishedRecord, error) {
	if !r.holdsKey(rec.Record.KeyID) {
		// Saying this before fetching is not an optimisation. SPEC.md 9 makes
		// the key id plaintext precisely so an instance can tell whether it
		// can read a record before spending a network round trip on it, and
		// the resulting message names a key rather than a decryption failure.
		return frontier.PublishedRecord{}, fmt.Errorf(
			"record %s is sealed under key %s, which this instance does not hold",
			rec.Record.RecordID, rec.Record.KeyID)
	}
	plaintext, err := sharedcatalog.OpenRecord(ctx, r.store, r.ring, rec.Record)
	if err != nil {
		return frontier.PublishedRecord{}, err
	}
	return frontier.DecodePublishedRecord(plaintext)
}

// holdsKey reports whether this instance's ring can open a record sealed under
// this key id. The ring lists its ids rather than exposing a lookup, so the
// scan is over a handful of entries and happens once per record.
func (r *Reader) holdsKey(id envelope.KeyID) bool {
	for _, held := range r.ring.KeyIDs() {
		if held == id {
			return true
		}
	}
	return false
}

// Hosts reports the machines that hold records matching filter, which is the
// vocabulary a host filter offers.
func (r *Reader) Hosts(ctx context.Context, filter sharedcatalog.RecordFilter) ([]sharedcatalog.RecordHost, error) {
	filter.DeploymentID = r.deployment
	return sharedcatalog.RecordHosts(ctx, r.db, filter)
}

// SyncStates resolves the sync state of durable records by global id, and is
// the one place that resolution lives.
//
// Four cases, in this order, and the order is the whole point:
//
//  1. A remote row exists under a committed run: the record is globally
//     durable and reviewable. "committed".
//  2. A remote row exists under a pending run: some of the closure committed
//     and some did not, so the record is staged remotely and not yet
//     reviewable. "pending-sync".
//  3. No remote row, and the journal has something to say: whatever it says.
//     This is the case only the journal can answer - a record staged while
//     PostgreSQL was unreachable has no remote row at all, and SPEC.md 9
//     requires it to be visibly pending rather than silently absent.
//  4. No remote row and no journal answer: SyncLocal. Not pending, because
//     nothing is going to carry it anywhere - either shared mode is not
//     configured or the record was never staged.
//
// A nil journal collapses 3 into 4, which is correct for a build with no
// publication journal wired: it genuinely cannot distinguish them, and
// answering SyncLocal says "nothing here claims this record is going anywhere",
// which is true.
func (r *Reader) SyncStates(ctx context.Context, journal SyncJournal, ids []string) (map[string]string, error) {
	remote, err := sharedcatalog.RecordSyncStates(ctx, r.db, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if state, found := remote[id]; found {
			out[id] = state
			continue
		}
		if journal != nil {
			state, err := journal.SyncState(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("read local sync state for %s: %w", id, err)
			}
			if state != "" {
				out[id] = state
				continue
			}
		}
		out[id] = SyncLocal
	}
	return out, nil
}

// LocalSyncStates resolves sync state with no catalog at all, which is what a
// listing in local mode needs.
//
// It is a function rather than a Reader method because a machine in local mode
// has no Reader: NewReader refuses without a catalog, an object store and a
// keyring, and a listing must still render a sync column. The answer is cases 3
// and 4 of the resolution above.
func LocalSyncStates(ctx context.Context, journal SyncJournal, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = SyncLocal
		if journal == nil {
			continue
		}
		state, err := journal.SyncState(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read local sync state for %s: %w", id, err)
		}
		if state != "" {
			out[id] = state
		}
	}
	return out, nil
}

// searchableKinds are the record kinds the retrieval index holds. Proposals and
// links commit to the shared catalog and are readable in a listing, but have no
// searchable output: a proposal's text is its findings' text restated for a
// reviewer, and indexing both would make every consolidated finding match twice
// and read as two independent prior ideas.
var searchableKinds = []sharedcatalog.RecordKind{
	sharedcatalog.KindHypothesis,
	sharedcatalog.KindObservation,
	sharedcatalog.KindFinding,
	sharedcatalog.KindDisposition,
}

// IngestReport is what one fleet ingest did, per host and in total.
type IngestReport struct {
	// Hosts maps each host id ingested to what its partition became.
	Hosts map[string]index.FrontierResult
	// Forgotten names the origins whose partitions were dropped because the
	// catalog no longer reports records for them. A cache eviction, never a
	// loss: the records remain in PostgreSQL and Cellar.
	Forgotten []string
	// Unattributed counts committed records skipped because their origin
	// instance has no registered host, so there is no partition to file them
	// under. It is reported rather than silent because the remedy is one push
	// from the owning host, and an operator cannot ask for it without knowing
	// the number.
	Unattributed int
	// Unopened lists per-record reasons content could not be read: a key this
	// instance does not hold, a payload from a newer build, a store failure.
	// Each costs one record and never the ingest.
	Unopened []string
}

// IngestOptions bounds one fleet ingest.
type IngestOptions struct {
	// Hosts narrows the ingest to specific machines. Empty ingests every host
	// that has committed searchable records.
	Hosts []string
	// Rebuild drops every remote partition before ingesting, which is the
	// answer to a cache that may hold rows from a build whose derivation has
	// since changed. It is safe by construction and cheap by design: SPEC.md
	// 14 settled local-only indexing on exactly this ground, that the index is
	// rebuildable and its loss costs a re-index rather than data. The local
	// partition is never touched.
	Rebuild bool
}

// Ingest reconciles this machine's retrieval index against the committed
// analysis of every host in the deployment (issue #109 item 4).
//
// This is what makes self-retrieval and dedup answer across hosts, so that two
// conductors on two machines cannot silently duplicate one another. It writes
// only to the index, which is why it is safe to run at any time and why losing
// its output costs a re-index: the durable stores are untouched, and a record
// another host owns is never copied into a table this machine would then try to
// publish.
//
// Each host's partition is reconciled against that host's complete committed
// set, fetched in pages until exhausted. Completeness is a requirement rather
// than an optimisation: the index deletes rows the offered set does not name,
// which is how a superseded wording stops being searchable, so a partial set
// would delete the remainder.
//
// Hosts the index holds and the catalog no longer reports are forgotten, so a
// retired machine stops answering as though it were still reporting. When
// Options.Hosts narrows the ingest, nothing is forgotten: a caller that asked
// about one machine has said nothing about the others.
func (r *Reader) Ingest(ctx context.Context, idx *index.Index, opts IngestOptions) (IngestReport, error) {
	report := IngestReport{Hosts: map[string]index.FrontierResult{}}
	if idx == nil {
		return report, errors.New("fleet ingest: an index is required")
	}

	if opts.Rebuild {
		origins, err := idx.FrontierOrigins(ctx)
		if err != nil {
			return report, err
		}
		for origin := range origins {
			if origin == index.LocalOrigin {
				continue
			}
			if _, err := idx.ForgetFrontierOrigin(ctx, origin); err != nil {
				return report, err
			}
			report.Forgotten = append(report.Forgotten, origin)
		}
	}

	byHost, unattributed, unopened, err := r.collect(ctx, opts.Hosts)
	if err != nil {
		return report, err
	}
	report.Unattributed, report.Unopened = unattributed, unopened

	for host, outputs := range byHost {
		res, err := idx.IndexFleetFrontier(ctx, host, outputs)
		if err != nil {
			return report, err
		}
		report.Hosts[host] = res
	}

	if len(opts.Hosts) == 0 {
		origins, err := idx.FrontierOrigins(ctx)
		if err != nil {
			return report, err
		}
		for origin := range origins {
			if origin == index.LocalOrigin {
				continue
			}
			if _, seen := byHost[origin]; seen {
				continue
			}
			if _, err := idx.ForgetFrontierOrigin(ctx, origin); err != nil {
				return report, err
			}
			report.Forgotten = append(report.Forgotten, origin)
		}
	}
	sort.Strings(report.Forgotten)
	sort.Strings(report.Unopened)
	return report, nil
}

// collect fetches every committed searchable record, opens it, and groups the
// flattened outputs by the host that produced it.
//
// The paging loop runs to exhaustion rather than to a cap. The bound that
// matters is the analysis a deployment has produced - thousands of records, in
// the same order of magnitude as the local durable store - and a cap would
// silently turn a complete reconcile into a destructive partial one.
func (r *Reader) collect(ctx context.Context, hosts []string) (
	byHost map[string][]frontier.Output, unattributed int, unopened []string, err error) {
	byHost = map[string][]frontier.Output{}
	filter := sharedcatalog.RecordFilter{
		DeploymentID: r.deployment,
		Hosts:        hosts,
		Kinds:        searchableKinds,
		Limit:        sharedcatalog.MaxRecordLimit,
	}
	for {
		page, err := r.Records(ctx, filter)
		if err != nil {
			return nil, 0, nil, err
		}
		for _, rec := range page {
			if rec.HostID == "" {
				unattributed++
				continue
			}
			published, err := r.Open(ctx, rec.FleetRecord)
			if err != nil {
				unopened = append(unopened, fmt.Sprintf("%s: %v", rec.Record.RecordID, err))
				continue
			}
			output, err := published.Output()
			if errors.Is(err, frontier.ErrNotSearchable) {
				// Normal rather than exceptional: the catalog carries kinds
				// with no retrieval surface, and meeting one is not a fault.
				continue
			}
			if err != nil {
				unopened = append(unopened, fmt.Sprintf("%s: %v", rec.Record.RecordID, err))
				continue
			}
			byHost[rec.HostID] = append(byHost[rec.HostID], output)
		}
		if len(page) < filter.Limit {
			return byHost, unattributed, unopened, nil
		}
		filter.Offset += len(page)
	}
}
