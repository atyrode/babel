package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/restic"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// maxHostDisplayNameLen bounds what this machine will assert as its display
// name. The column is unconstrained text, so the bound belongs at the writer:
// a display name is a short label a fleet listing puts beside a host id, and
// something longer is a mistake rather than a name.
const maxHostDisplayNameLen = 64

// hostIdentity is what this machine asserts about itself on every push
// (decision 8, migrations/0004).
//
// The display name is operator-assigned, from $BABEL_HOST_DISPLAY_NAME, and
// defaults to the host id this push is publishing under. Two things it is
// deliberately not:
//
//   - It is never the system hostname. Falling back to os.Hostname() would put
//     infrastructure identity into the shared catalog, which is the value
//     reconcile.go refuses to adopt snapshots recorded under, and which the
//     operator's plaintext decision did not cover.
//   - It is never empty. An empty assertion is silence in Register, which is
//     right for a machine with nothing to say, but this machine always has
//     something honest to say: the host id, already the primary key of the row
//     it is writing. So the column carries a readable name from the first push
//     rather than staying NULL until someone configures one.
//
// The operating system and architecture are this binary's build platform, the
// same pair `babel version` prints, so they cost no lookup and cannot fail.
func hostIdentity(host string) sharedcatalog.HostIdentity {
	// Truncation is by rune, not by byte: cutting mid-sequence would store an
	// invalid UTF-8 fragment, which PostgreSQL's text type rejects outright.
	name := strings.TrimSpace(os.Getenv("BABEL_HOST_DISPLAY_NAME"))
	if runes := []rune(name); len(runes) > maxHostDisplayNameLen {
		name = string(runes[:maxHostDisplayNameLen])
	}
	return sharedcatalog.HostIdentity{
		DisplayName: firstNonEmpty(name, host),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
	}
}

// Catalog states a push reports. They describe what happened to the shared
// catalog, never to the archive: the snapshot is already durable in the
// repository by the time any of this runs.
const (
	// catalogLocal means there is no shared catalog to publish into.
	catalogLocal = "local"
	// catalogCommitted means the snapshot and its session rows are visible to
	// every instance.
	catalogCommitted = "committed"
	// catalogUncatalogued means the snapshot is durable and the shared catalog
	// holds no row for it at all, which is what an outage or a lease already
	// held by another instance leaves behind. The next push records it, or
	// reconciliation adopts it from the repository's snapshot list without
	// republishing bytes (SPEC.md 9).
	//
	// It deliberately does NOT say "catalog-pending". That phrase names a
	// different state in this system: a catalog row that exists, carries
	// restic's real counts, and lacks any record of which sessions the snapshot
	// held. `archive status` reports the two separately because they call for
	// different responses, and a push that used the narrower word for the wider
	// state would send an operator looking for session detail that was never
	// written rather than for a row that was never created.
	catalogUncatalogued = "uncatalogued"
)

// publicationLeaseTTL bounds how long this instance may hold a host's lease.
//
// The lease is taken after restic has committed, so it covers one short
// transaction rather than the backup: a generous TTL would only delay another
// instance's takeover if this process died mid-write.
const publicationLeaseTTL = 2 * time.Minute

// publishToCatalog records a committed snapshot and this host's session
// identity in the shared catalog.
//
// It returns the catalog state to report and the number of session rows
// published. A returned error is a genuine failure; an outage is not one, and
// reports catalogUncatalogued instead.
func (a *app) publishToCatalog(
	ctx context.Context,
	d dirs,
	host string,
	repo *restic.Repo,
	summary *restic.BackupSummary,
) (state string, published int, err error) {
	cfg, found, err := config.Load()
	if err != nil {
		return "", 0, err
	}
	if !found || storageMode(cfg) != config.ModeShared || cfg.Catalog == nil {
		return catalogLocal, 0, nil
	}

	// Session identity is read through the same incremental cache `sessions
	// list` uses, so an hourly push describes what changed rather than the
	// whole corpus. The counts it carries were computed by that describe.
	sessions, covered := a.scan(ctx, adapters(), nil)
	rows, err := a.publishableSessions(ctx, sessions, covered, d.data, cfg.DeploymentID, host)
	if err != nil {
		return "", 0, err
	}

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
	if err != nil {
		return a.catalogDeferred(err, "reach the shared catalog")
	}
	defer db.Close()

	if err := sharedcatalog.Register(ctx, db, cfg.DeploymentID, host, cfg.InstanceID,
		hostIdentity(host)); err != nil {
		return a.catalogDeferred(err, "register with the shared catalog")
	}

	lease, err := sharedcatalog.AcquireHostLease(ctx, db, host, cfg.InstanceID, publicationLeaseTTL)
	if err != nil {
		return a.catalogDeferred(err, "take this host's publication lease")
	}
	defer func() {
		// Releasing early lets another instance publish without waiting out the
		// TTL. Failing to release is not worth failing a completed publication
		// over: the lease expires on its own.
		if relErr := sharedcatalog.ReleaseHostLease(ctx, db, lease); relErr != nil {
			a.diagf("warning: release publication lease: %s\n", Sanitize(relErr.Error()))
		}
	}()

	// One listing serves reconciliation and the snapshot's recorded time.
	// snapshot_time is restic's, not this process's clock: the column records
	// when the snapshot was made, and publication can happen much later after an
	// outage. If the repository cannot be listed the time is unknown, so
	// publication defers rather than substituting now().
	listing, err := repo.Snapshots(ctx)
	if err != nil {
		return a.catalogDeferred(err, "list the repository's snapshots")
	}

	// Adopt any earlier snapshot the repository holds that the catalog does not,
	// which is how a push catalogues snapshots that an outage stranded: SPEC.md
	// 9 makes the owning host's next push one of the two recovery paths.
	//
	// This runs before publishing, and excludes the snapshot being published,
	// for an ordering reason. Reconcile assigns each adopted snapshot the next
	// order above the current maximum, so adopting afterwards would give a
	// stranded OLDER snapshot a HIGHER publication_order than the one just
	// published - and publication_order is what totally orders a host's
	// snapshots so readers need not trust clock skew (migrations/0001_init.sql).
	// Adopting first, then taking the next order, keeps time order and
	// publication_order in agreement.
	//
	// Adopted snapshots carry their real counts from restic's own summary but no
	// session rows, so they stay catalog-pending until a push describes them.
	earlier := hostSnapshots(listing, host, summary.SnapshotID)
	if rep, err := sharedcatalog.Reconcile(ctx, db, host, earlier); err != nil {
		return a.catalogDeferred(err, "reconcile this host's earlier snapshots")
	} else if rep.Added > 0 {
		a.diagf("note: adopted %d earlier %s as catalog-pending\n",
			rep.Added, plural(rep.Added, "snapshot", "snapshots"))
	}

	order, err := sharedcatalog.NextPublicationOrder(ctx, db, host)
	if err != nil {
		return a.catalogDeferred(err, "read this host's publication order")
	}
	snapshotTime, err := snapshotTimeIn(listing, summary.SnapshotID)
	if err != nil {
		return a.catalogDeferred(err, "read the snapshot's recorded time")
	}

	snap := sharedcatalog.SnapshotRow{
		SnapshotID:       summary.SnapshotID,
		PublicationOrder: order,
		SnapshotTime:     snapshotTime,
		CommitState:      sharedcatalog.CommitCommitted,
		FilesNew:         int64(summary.FilesNew),
		FilesChanged:     int64(summary.FilesChanged),
		FilesUnmodified:  int64(summary.FilesUnmodified),
		BytesAdded:       summary.DataAdded,
		SessionCount:     len(rows),
		PublishedBy:      cfg.InstanceID,
	}

	// The snapshot id keys the publication. restic ids are content-addressed and
	// unique per snapshot, so one snapshot is one logical publication: a retried
	// push of the same snapshot is a no-op, and a new backup publishes anew.
	applied, err := sharedcatalog.PublishSnapshot(ctx, db, lease,
		"snapshot:"+summary.SnapshotID, snap, rows)
	if err != nil {
		return a.catalogDeferred(err, "publish to the shared catalog")
	}
	if !applied {
		a.diagf("note: snapshot %s was already published; the catalog was left unchanged\n",
			Sanitize(summary.SnapshotID))
	}
	return catalogCommitted, len(rows), nil
}

// snapshotTimeIn reads the recorded time of one snapshot from a listing.
//
// restic assigns it, and it is the value the catalog's snapshot_time column
// means. A snapshot that has just been created but is missing from the listing
// is a real inconsistency rather than an absent optional field, so it is an
// error.
func snapshotTimeIn(listing []restic.Snapshot, id string) (time.Time, error) {
	for _, s := range listing {
		if s.ID == id {
			return s.Time.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("the repository does not list snapshot %s", Sanitize(id))
}

// hostSnapshots restates this host's snapshots in the catalog's terms, omitting
// the ids in skip.
//
// Reconcile refuses a listing attributed to another host (ErrHostMismatch), and
// one repository holds every machine's snapshots, so the host filter is required
// rather than defensive. skip carries the snapshot about to be published, which
// must not be adopted first: adoption would record it as catalog-pending with a
// lower publication order, and the publication that follows would then be
// updating a row rather than creating one.
//
// Counts stay nil when restic recorded no summary: the catalog distinguishes an
// unknown count from a count of zero.
func hostSnapshots(listing []restic.Snapshot, host string, skip ...string) []sharedcatalog.RepoSnapshot {
	skipped := make(map[string]bool, len(skip))
	for _, id := range skip {
		skipped[id] = true
	}
	out := make([]sharedcatalog.RepoSnapshot, 0, len(listing))
	for _, s := range listing {
		if s.Host != host || skipped[s.ID] {
			continue
		}
		row := sharedcatalog.RepoSnapshot{SnapshotID: s.ID, Host: s.Host, Time: s.Time.UTC()}
		if s.Summary != nil {
			row.Counts = &sharedcatalog.SnapshotCounts{
				FilesNew:        int64(s.Summary.FilesNew),
				FilesChanged:    int64(s.Summary.FilesChanged),
				FilesUnmodified: int64(s.Summary.FilesUnmodified),
				BytesAdded:      s.Summary.DataAdded,
			}
		}
		out = append(out, row)
	}
	return out
}

// catalogDeferred decides whether a catalog failure defers publication or fails
// the push.
//
// Deferring is only honest when reconciliation can finish the job. An outage
// qualifies: the snapshot is durable and any instance can adopt it from the
// repository's snapshot list. Lease contention qualifies too - another instance
// is publishing for this host right now, and its push or a later reconciliation
// will carry this snapshot.
//
// Everything else - a rejected credential, a missing privilege, a pending
// migration, a schema this binary cannot write - would defeat reconciliation in
// exactly the same way, so reporting a state that appears to resolve itself
// would hide a misconfiguration. Those fail.
func (a *app) catalogDeferred(err error, what string) (string, int, error) {
	switch {
	case sharedcatalog.Unreachable(err):
		a.diagf("warning: could not %s: %s\n", what, Sanitize(err.Error()))
		a.diagf("note: the snapshot is durable; run `babel archive push` again or reconcile to catalogue it\n")
		return catalogUncatalogued, 0, nil
	case errors.Is(err, sharedcatalog.ErrLeaseHeld), errors.Is(err, sharedcatalog.ErrLeaseLost):
		a.diagf("warning: another instance is publishing for this host: %s\n", Sanitize(err.Error()))
		a.diagf("note: the snapshot is durable and will be catalogued by the next push or reconciliation\n")
		return catalogUncatalogued, 0, nil
	}
	return "", 0, fmt.Errorf("%s: %w", what, err)
}

// publishableSessions turns this host's live sessions into catalog rows.
//
// What crosses this boundary is opaque identity, counts, and the browsable
// metadata the operator admitted on 2026-08-30: title, workspace, and
// continuation grade (migrations/0004). The adapter source id still does not:
// SessionUID is a digest over the deployment, host, harness and source id, and
// the source id is also the fetch selector, so keeping it out is what makes
// resolving a uid to something fetchable need the repository or a local index.
//
// Nothing here re-reads a session to obtain these values. They were computed by
// the describe that filled the local cache, which is what keeps an hourly push
// scaling with what changed rather than with the whole corpus.
//
// The host is the one this push is publishing under - the resolved identity that
// took the lease and that restic recorded on the snapshot - not the configured
// default. They differ whenever `--host` or $BABEL_HOST_ID overrides
// storage.json, and deriving session identity from the configured value would
// attribute a host's sessions to an identity that never published them, which
// breaks the uniqueness the digest exists to provide (decision 9).
func (a *app) publishableSessions(
	ctx context.Context,
	sessions []localSession,
	covered []string,
	dataDir string,
	deploymentID string,
	host string,
) ([]sharedcatalog.SessionRow, error) {
	cache, err := catalog.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open session catalog: %w", err)
	}
	defer cache.Close()

	refs, bySelector := catalogRefs(sessions)
	cached, err := cache.Refresh(ctx, refreshScope(covered, nil), refs,
		a.catalogDescriber(ctx, bySelector, describe), nil)
	if err != nil {
		return nil, fmt.Errorf("refresh session catalog: %w", err)
	}

	// A title the operator paid a model for is published like any other: it is
	// the best name this host has for the session, and the shared row is what
	// another machine reads instead of the transcript. What travels with it is
	// the "inferred" provenance, so no reader can mistake a guess for a record.
	overlay := a.loadInferredOverlay(ctx, dataDir)

	rows := make([]sharedcatalog.SessionRow, 0, len(cached))
	for _, row := range cached {
		// The cache holds the whole machine; a push publishes what this scan
		// actually saw, so a restricted scan cannot silently claim coverage.
		if _, ok := bySelector[row.Selector]; !ok {
			continue
		}
		// The grade is a plain bool locally because this machine always
		// resolves it: it comes from artifact closure and unresolved blobs in
		// files this host holds. It is published as a pointer because the
		// catalog's column is nullable, and NULL there means "no host has
		// supplied this" - a state every reader but this one can meet. A push
		// from here therefore never writes NULL: it knows the answer.
		grade := row.ContinuationGrade
		publishTitle, publishProvenance := overlay.apply(row.Selector, row.Title, row.TitleProvenance)
		rows = append(rows, sharedcatalog.SessionRow{
			SessionUID: sharedcatalog.SessionUID(
				deploymentID, host, row.Harness, row.SourceID),
			Harness:             row.Harness,
			PrimarySize:         row.PrimarySize,
			ArtifactCount:       row.ArtifactCount,
			BlobCount:           row.BlobCount,
			UnresolvedBlobCount: row.UnresolvedBlobCount,
			SourceModifiedAt:    parseCatalogTime(row.ModifiedAt),
			// Title, TitleProvenance and Workspace stay nil when the adapter
			// reported none. The cache already distinguishes that from an
			// empty string, and flattening it here would put "" in a column
			// whose NULL means unknown.
			//
			// The provenance travels with the title because the reader who
			// most needs it is the one furthest from the session: an instance
			// on another machine sees this row and nothing else, and a title
			// there with no provenance is a claim it cannot check.
			Title:             publishTitle,
			TitleProvenance:   publishProvenance,
			Workspace:         row.Workspace,
			ContinuationGrade: &grade,
			// The usage summary travels as the cache holds it: nil when this
			// host's adapter extracted none, never flattened to zero. A
			// reader on another machine has no transcript to recompute from,
			// so a zero here would be the only thing it could believe.
			CostUSD:     row.CostUSD,
			TotalTokens: row.TotalTokens,
			Turns:       row.Turns,
			ToolErrors:  row.ToolErrors,
		})
	}
	return rows, nil
}

// parseCatalogTime restores a cached timestamp. An unparseable or absent value
// stays absent: the column distinguishes unknown from zero, and inventing a
// time would be worse than admitting the adapter reported none.
func parseCatalogTime(value *string) *time.Time {
	if value == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return &t
}
