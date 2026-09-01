package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/sharedcatalog"
	// internal/sync is imported under a name of its own because this package
	// already imports the standard library's `sync` in scan.go and web.go. One
	// import name for one package across internal/cli is worth more than the
	// four characters brevity would save: a name that changes from file to
	// file is a name an operator reading the second file has to re-learn.
	babelsync "github.com/atyrode/babel/internal/sync"
)

const syncUsage = `Usage: babel sync [flags]

Publishes this machine's durable Phase B records - hypotheses, observations,
findings, proposals, links, dispositions, operator context, preparations and
run receipts - to the shared backend, and reports what is still owed to the
fleet.

Every record was already durable locally before this ran, so nothing here can
lose one: a record this attempt cannot publish stays durable and visibly
pending, and the next attempt carries it. A local-only deployment has nothing
to publish, says so, and exits 0.

Flags:
  --generate-key ID           create the payload key document with one fresh
                              AES-256 key under this id, and publish nothing
  --json                      emit the report as JSON
`

// syncResult is `babel sync`'s machine-readable outcome.
//
// It reports both halves of the answer, because a document that named only
// what it committed would leave "and what is still stuck" to be inferred from
// silence - the opposite of SPEC.md §9's requirement that staged Phase B
// output stay visibly pending.
type syncResult struct {
	// Configured is false when this deployment publishes nothing at all, which
	// is a supported state rather than a failure: Reason then names which
	// absence it is, and every count below is zero because nothing was
	// attempted.
	Configured bool   `json:"configured"`
	Reason     string `json:"reason,omitempty"`
	// Committed and Pending are per-kind counts in sorted kind order, so two
	// reports of the same state are byte-identical and a diff between them
	// means something moved.
	Committed      []syncKindRow `json:"committed"`
	Pending        []syncKindRow `json:"pending"`
	RunsCommitted  int           `json:"runs_committed"`
	RunsPending    int           `json:"runs_pending"`
	ObjectsWritten int           `json:"objects_written"`
	// Undeclared counts staged records whose producing run has not finished.
	// They are deliberately unpublishable rather than stuck; see writeSync.
	Undeclared int `json:"undeclared"`
	// Failures is a list rather than one error because one unreachable object
	// must not hide the nine closures that published.
	Failures []syncFailureRow `json:"failures,omitempty"`
}

// syncKindRow is one record kind's count.
type syncKindRow struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// syncFailureRow is one closure that did not publish. The run id is what makes
// the reason actionable: the same unreachable endpoint reads very differently
// depending on which run is still owed.
type syncFailureRow struct {
	RunID string `json:"run_id"`
	Error string `json:"error"`
}

// syncKeyResult is what `babel sync --generate-key` wrote.
//
// It carries the document's path and the key's id and never the key material.
// The id is admitted in plaintext beside every ciphertext in the shared
// catalog (SPEC.md §9); the material is the one value in Babel that no report,
// diagnostic or machine-readable document may ever contain.
type syncKeyResult struct {
	Path  string `json:"path"`
	KeyID string `json:"key_id"`
}

// syncCmd implements `babel sync`: publish every declared closure the local
// journal still owes the shared backend, and report what remains.
//
// It exits 0 whenever the attempt ran, failures included. A pending record is
// a state and not a command failure - the record is durable, the report names
// it, and the next attempt carries it - so an exit code that moved on one
// would make a scheduled sync look broken every time an endpoint blinked. Only
// a journal this machine cannot read, or a configuration that names a backend
// it cannot open, is a failure.
func (a *app) syncCmd(ctx context.Context, args []string) error {
	c := newCmd("sync", syncUsage)
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	keyID := c.fs.String("generate-key", "", "create the payload key document with one fresh key under this id")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	// An explicitly empty --generate-key is a rejected invocation rather than
	// an absent flag: `--generate-key=` asks for a key with no id, and reading
	// it as "no key was requested" would silently run a publication instead of
	// refusing the id.
	generate := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == "generate-key" {
			generate = true
		}
	})
	if generate {
		return a.generatePayloadKey(c, *keyID, *asJSON)
	}

	reason, err := syncUnavailable()
	if err != nil {
		return err
	}
	if reason != "" {
		if *asJSON {
			return a.emitJSON(syncResult{Reason: reason})
		}
		a.diagf("note: %s\n", reason)
		return nil
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	pub, cleanup, err := a.openPublisher(ctx, d)
	defer cleanup()
	if err != nil {
		return err
	}
	// Retry on a nil publisher is an empty report and no error, which is the
	// only outcome the configuration changing between the check above and the
	// call can produce. Nothing was staged by this process, so an empty report
	// is the honest one and no branch is needed to produce it.
	rep, err := pub.Retry(ctx)
	if err != nil {
		return fmt.Errorf("read the sync journal: %w", err)
	}
	res := syncReport(rep)
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeSync(res)
}

// stagingHook is the Phase B publication hook every durable writer on this
// machine opens with (#137): a staging hook in shared mode, and nothing at all
// in local mode.
//
// It stages and never publishes, and that is the decision this whole wiring
// turns on. internal/sync splits publication into a transaction-local half and
// a networked one: staging writes journal rows inside the writer's own
// transaction on the writer's own connection, while committing a closure needs
// PostgreSQL, the object store and the payload keyring. Only the first half
// belongs at store-open time. Handing `babel tell` a full publisher would make
// an operator's one-sentence complaint dial the catalog before it could be
// recorded - slow on a good day, and on a bad one a command that would rather
// fail than write, which is the exact inversion SPEC.md §6.5 forbids. So the
// writers stage, `babel sync` and the reconcile step after an archive push hold
// the publisher, and the journal is the handoff between them.
//
// The gate is mode and nothing else, which is why it is not openPublisher's.
// A shared deployment with no catalog reachable, or one whose payload keys have
// not arrived, publishes nothing and must still stage everything: a record owed
// to the fleet has to be visibly pending rather than silently local (SPEC.md
// §9, runbook §9.1), and staging needs neither of those two things to say so.
//
// The nil is a nil interface and never a typed nil pointer. Every store tests
// its hook for nil to decide whether it publishes at all, and an interface
// holding a nil *Stager would pass that test and then stage into nothing -
// which is precisely the silence #137 was.
//
// A configuration that will not load is returned as an error rather than
// degraded into local mode. An absent storage.json loads cleanly as "not
// configured", so this cannot fire on a local-only machine; what it does fire
// on is a present document this build cannot read, where the mode is unknown,
// and staging nothing on an unknown mode is how a shared host silently stops
// owing the fleet its records.
func stagingHook() (babelsync.Hook, error) {
	cfg, found, err := config.Load()
	if err != nil {
		return nil, err
	}
	if stagingUnavailable(cfg, found) != "" {
		return nil, nil
	}
	return babelsync.NewStager(), nil
}

// stagingUnavailable names why this deployment's durable writers stage nothing,
// and returns the empty string when they stage.
//
// It is syncUnavailable's mode half, split out rather than repeated because the
// two questions have the same answer only at the top: what publishes must be in
// shared mode, and what is in shared mode stages whether or not it can publish.
// Keeping one definition of the mode decision is what stops a writer and
// `babel sync` from disagreeing about which deployments are local-only.
func stagingUnavailable(cfg config.Config, found bool) string {
	switch {
	case !found:
		return "storage is not configured, so this machine is local-only and owes the fleet nothing"
	case storageMode(cfg) != config.ModeShared:
		return fmt.Sprintf("storage is configured in %s mode, so there is nothing to publish",
			Sanitize(storageMode(cfg)))
	}
	return ""
}

// syncUnavailable names why this deployment publishes nothing, and returns the
// empty string when it does publish.
//
// The three absences are conditions rather than faults and each has its own
// remedy, so each is named separately: a local-only deployment is finished and
// owes the fleet nothing, a shared one with no catalog is misconfigured
// somewhere this command cannot fix, and a shared one with no payload keys is
// one command away. SPEC.md §9 requires the last of those to be visible rather
// than fatal, which is why it is a line and an exit code of 0 rather than a
// refusal - a writer keeps writing, and its records stay durable and pending.
//
// This is the one place publication's absence is decided. openPublisher gates on
// it too, so the constructor and the report cannot come to different conclusions
// about what local-only means.
func syncUnavailable() (string, error) {
	cfg, found, err := config.Load()
	if err != nil {
		return "", err
	}
	if reason := stagingUnavailable(cfg, found); reason != "" {
		return reason, nil
	}
	if cfg.Catalog == nil {
		return "shared mode is configured with no catalog, so there is nowhere to publish to", nil
	}
	if _, found, err := config.LoadPayloadKeys(); err != nil {
		return "", err
	} else if !found {
		return fmt.Sprintf("no payload key document at %s, so no record can be sealed; "+
			"`babel sync --generate-key ID` creates one", Sanitize(config.PayloadKeysPath())), nil
	}
	return "", nil
}

// openPublisher builds the Phase B publisher this deployment's configuration
// describes, or reports why publication is not available.
//
// A nil publisher with a nil error means there is nothing to publish into, and
// every caller reads that as nothing to do. It is what local mode, an absent
// catalog and an absent payload key document all produce, and none of them is
// an error: a local-only deployment is a supported deployment, and a shared one
// that has not been given its keys yet is a state SPEC.md §9 requires to be
// visible rather than fatal. An error is the other thing entirely - a catalog
// this binary cannot reach, a key document it cannot read, a durable file it
// cannot write - and a caller that publishes on a schedule needs the two kept
// apart.
//
// The returned cleanup is never nil and is safe to call once on every path,
// including every error path, so a caller defers it immediately after the call
// and needs no branch to decide whether anything was opened.
//
// storage.json is read twice on the path that publishes: once by
// syncUnavailable's gate, once for the identity and locators assembled below.
// That is two reads of one small document in a command that is about to talk to
// PostgreSQL over the network, and it buys a single place that decides what
// "nothing to publish" means.
func (a *app) openPublisher(ctx context.Context, d dirs) (*babelsync.Publisher, func(), error) {
	var release []func()
	cleanup := func() {
		// Reverse acquisition order, and emptied afterwards so a second call
		// releases nothing twice.
		for i := len(release) - 1; i >= 0; i-- {
			release[i]()
		}
		release = nil
	}

	reason, err := syncUnavailable()
	if err != nil {
		return nil, cleanup, err
	}
	if reason != "" {
		return nil, cleanup, nil
	}
	cfg, _, err := config.Load()
	if err != nil {
		return nil, cleanup, err
	}

	// The journal lives in the durable database every Phase B component
	// shares, which is the directory internal/frontier and internal/run are
	// opened in: one file is what an operator has to preserve, and a journal
	// that could be lost independently of the records it tracks would leave
	// those records unpublishable with nothing saying so.
	journal, err := babelsync.OpenJournal(d.durableDir())
	if err != nil {
		return nil, cleanup, err
	}
	release = append(release, func() {
		if err := journal.Close(); err != nil {
			a.diagf("warning: release the sync journal: %s\n", Sanitize(err.Error()))
		}
	})

	// Returned unwrapped: sharedcatalog.Open's own error already names what it
	// was doing and whether the endpoint was unreachable, and prefixing it
	// again would put the same clause in the operator's line twice.
	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
	if err != nil {
		return nil, cleanup, err
	}
	release = append(release, func() {
		if err := db.Close(); err != nil {
			a.diagf("warning: release the shared catalog handle: %s\n", Sanitize(err.Error()))
		}
	})

	// The object store has nothing to release: both implementations hold a
	// signing client or a directory path rather than a descriptor, so there is
	// no Close for cleanup to call.
	store, err := objectstore.Open(cfg)
	if err != nil {
		return nil, cleanup, err
	}

	keys, found, err := config.LoadPayloadKeys()
	if err != nil {
		return nil, cleanup, err
	}
	if !found {
		// The gate above already refused this, so reaching it means the
		// document was removed between the two reads. That is still an
		// absence rather than a fault.
		return nil, cleanup, nil
	}
	active, material, err := keys.Material()
	if err != nil {
		return nil, cleanup, err
	}
	ring, err := envelope.RingFrom(envelope.KeyID(active), payloadKeyring(material))
	if err != nil {
		return nil, cleanup, err
	}

	pub, err := babelsync.New(babelsync.Options{
		Config:  cfg,
		Journal: journal,
		Catalog: db,
		Store:   store,
		Keyring: ring,
		// The one diagnostic line SPEC.md §8 allows a publication failure. The
		// publisher hands out an error rather than formatting one itself
		// because the value may carry a remote endpoint's words, and this
		// package owns the only renderer allowed to put such a value on a
		// terminal.
		Diag: func(err error) { a.diagf("babel: sync: %s\n", Sanitize(err.Error())) },
	})
	if err != nil {
		return nil, cleanup, err
	}
	return pub, cleanup, nil
}

// payloadKeyring restates decoded key material in internal/envelope's own key
// type.
//
// The two packages keep their own types deliberately - internal/config
// implements no cryptography and internal/envelope reads no document - so the
// conversion is the seam between them, and it belongs to the consumer that
// needs both.
func payloadKeyring(material map[string][]byte) map[envelope.KeyID][]byte {
	keys := make(map[envelope.KeyID][]byte, len(material))
	for id, key := range material {
		keys[envelope.KeyID(id)] = key
	}
	return keys
}

// generatePayloadKey creates the payload key document with one fresh AES-256
// key and reports where it went.
//
// It publishes nothing and does not require shared mode: a deployment is given
// its keys before it is given a catalog as often as after, and refusing to
// create the document until the rest of the configuration exists would make
// the order of two independent steps load-bearing for no reason.
func (a *app) generatePayloadKey(c *cmd, id string, asJSON bool) error {
	material, err := envelope.GenerateKey()
	if err != nil {
		return err
	}
	// The id is the only caller-supplied input here - the material is
	// internal/envelope's own key length by construction - so a refusal from
	// GeneratePayloadKey is a refusal of the id, which makes it a rejected
	// invocation rather than a failure. Its message already states the rule.
	key, err := config.GeneratePayloadKey(id, material)
	if err != nil {
		return c.usagef("%s", err)
	}
	// KeySchema is left unset: the writer stamps the version it writes, and a
	// caller that named one would be asserting a schema it does not own.
	if err := config.SavePayloadKeys(config.PayloadKeys{
		ActiveKeyID: key.KeyID,
		Keys:        []config.PayloadKey{key},
	}); err != nil {
		if errors.Is(err, config.ErrPayloadKeysExist) {
			// Replacing the document is refused rather than confirmed, and the
			// remedy is in the message because the failure is otherwise
			// invisible: every sealed object ever written under the keys it
			// holds needs them, Babel never deletes a remote object, and so
			// those objects would stay unreadable forever. Rotation appends a
			// key by editing the document; it never replaces the file.
			return fmt.Errorf("%s already holds this deployment's payload keys, and replacing it "+
				"would leave every object sealed under them unreadable forever; add a key to that "+
				"document to rotate instead", Sanitize(config.PayloadKeysPath()))
		}
		return err
	}

	res := syncKeyResult{Path: Sanitize(config.PayloadKeysPath()), KeyID: Sanitize(key.KeyID)}
	if asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "payload key %s written to %s\n", res.KeyID, res.Path)
	a.diagf("note: back up that document; every object sealed under a key it holds is unreadable without it\n")
	// The second note is the step this key is not finished without, and it is
	// on stderr because it is an instruction rather than the result.
	//
	// A key that exists on one disk is one disk failure away from every record
	// sealed under it being unreadable, and until the ring reaches the rest of
	// the fleet no other host can open those records at all — proven in #111,
	// where a host without the ring reads every plaintext row and no content.
	// Babel stays vault-agnostic (SPEC.md decisions 38, 50 and 51): it never
	// learns what a vault is, so this names the document field and the command
	// that install a ring on a machine and leaves naming the custodian to
	// whatever runs the ceremony. It names the file to copy from rather than
	// printing the ring, because the material reaches no stream, ever.
	a.diagf("note: this host is the only place %s exists; put the ring from %s into the deployment's "+
		"custody document as its \"payload_keys\" field, then re-provision the fleet — "+
		"`babel storage configure --from-json` is what installs it on every other host\n",
		res.KeyID, res.Path)
	return nil
}

// syncReport restates one Retry as this command's document.
func syncReport(rep babelsync.Report) syncResult {
	res := syncResult{
		Configured:     true,
		Committed:      syncKindRows(rep.Committed),
		Pending:        syncKindRows(rep.Pending),
		RunsCommitted:  rep.RunsCommitted,
		RunsPending:    rep.RunsPending,
		ObjectsWritten: rep.ObjectsWritten,
		Undeclared:     rep.Undeclared,
	}
	for _, f := range rep.Failures {
		// The run id and the reason both come out of the journal or out of a
		// remote endpoint, so both are rendered rather than trusted.
		res.Failures = append(res.Failures, syncFailureRow{
			RunID: Sanitize(f.RunID),
			Error: Sanitize(f.Err.Error()),
		})
	}
	return res
}

// syncKindRows renders a per-kind count in sorted kind order.
//
// Sorting is what makes two reports of the same state byte-identical, so a
// diff between them means something moved rather than that a map iterated
// differently. A kind with no records is absent rather than reported as zero:
// the journal counts only the kinds it holds, and printing a zero for the
// eight kinds a deployment does not produce would bury the one that matters.
func syncKindRows(counts map[sharedcatalog.RecordKind]int) []syncKindRow {
	if len(counts) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	rows := make([]syncKindRow, 0, len(kinds))
	for _, kind := range kinds {
		// The kind is read back out of a local SQLite file rather than
		// recomputed, so it is an untrusted value like any other.
		rows = append(rows, syncKindRow{
			Kind:  Sanitize(kind),
			Count: counts[sharedcatalog.RecordKind(kind)],
		})
	}
	return rows
}

// syncTotal sums a per-kind count.
func syncTotal(rows []syncKindRow) int {
	total := 0
	for _, row := range rows {
		total += row.Count
	}
	return total
}

// writeSync renders one attempt for a terminal.
//
// The summary comes first and answers "did anything move" by itself, so the
// table below it is confirmation rather than work - the same shape `archive
// fleet` reports in. The failures get a table of their own because a reason is
// long and a run id is not, and folding them into the detail block would push
// its field column open for the one closure that failed.
//
// The undeclared sentence goes to stderr with the rest of Babel's prose. The
// count belongs in the report, but what the count means takes a sentence, and
// a sentence inside an aligned block is what makes the block stop aligning.
func (a *app) writeSync(res syncResult) error {
	committed, pending := syncTotal(res.Committed), syncTotal(res.Pending)
	fmt.Fprintf(a.stdout, "published %d %s in %d %s; %d still pending\n\n",
		committed, plural(committed, "record", "records"),
		res.RunsCommitted, plural(res.RunsCommitted, "run", "runs"),
		pending)

	rows := make([][2]string, 0, len(res.Committed)+len(res.Pending)+4)
	for _, row := range res.Committed {
		rows = append(rows, [2]string{"committed " + row.Kind, fmt.Sprint(row.Count)})
	}
	rows = append(rows,
		[2]string{"runs committed", fmt.Sprint(res.RunsCommitted)},
		[2]string{"objects written", fmt.Sprint(res.ObjectsWritten)},
	)
	for _, row := range res.Pending {
		rows = append(rows, [2]string{"pending " + row.Kind, fmt.Sprint(row.Count)})
	}
	rows = append(rows,
		[2]string{"runs pending", fmt.Sprint(res.RunsPending)},
		[2]string{"undeclared records", fmt.Sprint(res.Undeclared)},
	)
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}

	if len(res.Failures) > 0 {
		fmt.Fprintln(a.stdout)
		failures := make([][]string, 0, len(res.Failures))
		for _, f := range res.Failures {
			failures = append(failures, []string{f.RunID, f.Error})
		}
		if err := writeTable(a.stdout, []string{"RUN NOT PUBLISHED", "REASON"}, failures); err != nil {
			return err
		}
	}
	if res.Undeclared > 0 {
		a.diagf("note: %d staged %s to a run that has not finished; they publish as soon as it "+
			"does, and are never dropped\n",
			res.Undeclared, plural(res.Undeclared, "record belongs", "records belong"))
	}
	return nil
}

// syncAfterPush publishes this host's durable Phase B records as the last step
// of an archive push, and never fails it.
//
// It runs after the Phase A catalog reconcile because the two are independent
// and the ordering is what keeps them so: reconcile finishes the snapshot's own
// publication while it holds this host's lease, and Phase B records are owed to
// the fleet whether or not a snapshot was taken. Riding the same invocation is
// what makes an hourly push enough to keep a workstation's analysis output off
// that workstation's disk, with no second timer to configure.
//
// It cannot fail the push, change its exit code, or change its reported catalog
// state. The snapshot is already durable in restic and the records are already
// durable here and visibly pending-sync, so a Phase B outage costs a later
// `babel sync` and nothing else - while a push whose exit code moved on it
// would report a failure that is not one, and teach an operator to distrust the
// exit code of the one command that guarantees the archive.
func (a *app) syncAfterPush(ctx context.Context, d dirs) {
	pub, cleanup, err := a.openPublisher(ctx, d)
	defer cleanup()
	if err != nil {
		a.diagf("warning: could not publish durable records: %s\n", Sanitize(err.Error()))
		a.diagf("note: they stay durable and pending; run `babel sync` once the reason is fixed\n")
		return
	}
	if pub == nil {
		// Local mode, no catalog, or no payload key document. `babel sync` is
		// where that absence is explained; a push says nothing about it,
		// because a local-only deployment's push is not missing anything.
		return
	}
	rep, err := pub.Retry(ctx)
	if err != nil {
		a.diagf("warning: could not read the sync journal: %s\n", Sanitize(err.Error()))
		return
	}
	res := syncReport(rep)
	committed, pending := syncTotal(res.Committed), syncTotal(res.Pending)
	if committed == 0 && pending == 0 && len(res.Failures) == 0 {
		// A push that had no durable records to carry says nothing, on the
		// same terms as the catalog row a local-mode push omits: an operator
		// reading an hourly log does not need a line reporting two zeroes.
		return
	}
	a.diagf("note: published %d durable %s to the shared catalog; %d still pending\n",
		committed, plural(committed, "record", "records"), pending)
}
