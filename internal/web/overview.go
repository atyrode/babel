package web

// GET /api/overview: the dashboard's one aggregate read.
//
// The dashboard is a landing page, not a seventh source of truth. Every number
// here is read from the services this server already holds, through the same
// interfaces the owning pages read, and nothing here starts work: no model is
// invoked, no exploration begins, and no store is written. What the operator
// sees is durable state that already existed before the page was opened.
//
// The response is one document with independently degrading sections. A
// dashboard that refused the whole snapshot because one store would not open
// would take away the panels that did have answers, which is the opposite of
// what a landing page is for, so each section carries its own availability and
// an authorized caller always gets 200. The wording of an unavailable section
// is fixed text, never a service error's own message: §9 keeps credentials,
// paths and corpus text out of what a client is told, and a section note is
// read by exactly the same rule as a refusal.

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
)

const (
	// overviewRows bounds how many rows one panel shows. A panel is a
	// glance: it states a total and then the few most recent or most
	// relevant records, and the page it links to is where a listing is
	// read.
	overviewRows = 5

	// overviewArchiveBudget bounds the repository read. Snapshot status
	// goes over the network to restic, so on a remote repository it is by
	// far the slowest thing this document assembles. The bound is what
	// keeps the landing page from hanging on an unreachable repository: the
	// archive panel degrades and the other five still arrive.
	overviewArchiveBudget = 8 * time.Second

	// overviewRecipeProbe bounds how many of a run's candidates are read
	// for §5.1 recipe provenance. The recipe a run applied is not in a
	// receipt's header — §9's plaintext allowlist keeps the cookbook
	// identities in the sealed body, and RunSummary carries nothing from
	// there — but an observation records the recipe that produced it, and
	// the cookbook is public. So the provenance is read from the frontier
	// instead, from a few of the run's own candidates rather than all of
	// them, because a panel row needs the recipe's identity and not a
	// census of it.
	overviewRecipeProbe = 3
)

// overviewSection is one panel's availability. Available is false with a note
// whenever the section could not be read, which is a different fact from a
// section that read successfully and found nothing.
type overviewSection struct {
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
}

func sectionReady() overviewSection { return overviewSection{Available: true} }

func sectionMissing(note string) overviewSection {
	return overviewSection{Available: false, Unavailable: note}
}

// overview is the whole snapshot the dashboard renders.
type overview struct {
	Archive  overviewArchive  `json:"archive"`
	Corpus   overviewCorpus   `json:"corpus"`
	Frontier overviewFrontier `json:"frontier"`
	Review   overviewReview   `json:"review"`
	Runs     overviewRuns     `json:"runs"`
	Activity overviewActivity `json:"activity"`
}

// overviewArchive is the repository's health as the archive surface knows it.
type overviewArchive struct {
	overviewSection
	Configured bool                  `json:"configured"`
	Repository string                `json:"repository"`
	HostID     string                `json:"host_id"`
	Snapshots  int                   `json:"snapshots"`
	LatestTime string                `json:"latest_time"`
	Hosts      []overviewArchiveHost `json:"hosts"`
	HostsTotal int                   `json:"hosts_total"`
	// Uncatalogued and Pending are null when the shared catalog was not
	// read, which is the CLI's own convention and the honest one: a local
	// deployment has no catalog to be behind, and an unreachable one makes
	// the counts unknown rather than zero.
	Uncatalogued *int  `json:"uncatalogued"`
	Pending      *int  `json:"pending"`
	Reachable    *bool `json:"catalog_reachable"`
}

type overviewArchiveHost struct {
	Host          string `json:"host"`
	Snapshots     int    `json:"snapshots"`
	LatestTime    string `json:"latest_time"`
	LatestShortID string `json:"latest_short_id"`
}

// overviewCorpus is what this machine's catalog holds, and how much of it Babel
// has actually described.
type overviewCorpus struct {
	overviewSection
	Sessions  int               `json:"sessions"`
	Titled    int               `json:"titled"`
	Harnesses []overviewHarness `json:"harnesses"`
	// Recorded, Derived and Inferred split the titles by where they came
	// from. The distinction is the point: a harness-recorded title, a title
	// Babel derived from the session's own records, and one a model
	// inferred are three different kinds of claim, and a coverage number
	// that merged them would report a model's guess as the corpus's own
	// name for a session.
	Recorded    int       `json:"recorded"`
	Derived     int       `json:"derived"`
	Inferred    int       `json:"inferred"`
	RefreshedAt string    `json:"refreshed_at"`
	Scan        ScanState `json:"scan"`
	// Pending is how many sessions the running scan has still to describe.
	Pending int `json:"pending"`
}

type overviewHarness struct {
	Harness  string `json:"harness"`
	Sessions int    `json:"sessions"`
	Titled   int    `json:"titled"`
}

// overviewFrontier is the hypothesis frontier's shape: how many candidates
// exist, how they are distributed across §4.2's lifecycle, and the newest few
// in the model's own wording.
type overviewFrontier struct {
	overviewSection
	Hypotheses int `json:"hypotheses"`
	// Statuses carries all six exploration statuses in §4.2 order, zeros
	// included. A distribution that listed only the non-empty statuses
	// would quietly drop "rejected" on a frontier where nothing had been
	// rejected yet, and §5.2's rule is that rejection stays visible.
	Statuses []overviewStatusCount `json:"statuses"`
	// Truncated says the counts are a floor rather than a total, because
	// enumeration hit its own bound.
	Truncated bool                 `json:"truncated"`
	Rows      []overviewHypothesis `json:"rows"`
}

type overviewStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type overviewHypothesis struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	// Statement is the candidate as the model worded it, whole and
	// untruncated (§5.2). It is untrusted text and is escaped by writeJSON
	// like every other value on this surface.
	Statement string `json:"statement"`
}

// overviewReview is what is waiting on a human: records awaiting a disposition,
// and the ledger questions only an operator can move.
type overviewReview struct {
	overviewSection
	Awaiting  int                 `json:"awaiting"`
	Rows      []overviewReviewRow `json:"rows"`
	Questions overviewQuestions   `json:"questions"`
	// Dispositions is #87's proposed next actions waiting for an answer. It
	// is a section of its own for the reason Questions is: the ledger of
	// proposed actions is a different component of the durable file, so a
	// machine can have a review log and no answer to give about actions,
	// and the panel then shows one and says so about the other.
	Dispositions overviewDispositions `json:"dispositions"`
}

// overviewDispositions counts what #87 put in front of the operator: actions a
// run proposed that nobody has accepted or declined yet.
//
// Only the pending count is here. A dashboard panel is a glance, and the
// records the actions belong to are what the record pages show; a landing page
// that listed every proposed action would be a second review queue with a
// different vocabulary.
type overviewDispositions struct {
	overviewSection
	Pending int `json:"pending"`
}

type overviewReviewRow struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	EnrolledAt string `json:"enrolled_at"`
	// Excerpt is the record's own line — a candidate's statement, an
	// observation's claim, a title — never a summary of it.
	Excerpt string `json:"excerpt"`
}

// overviewQuestions is the §4.8 inbox. It is a section of its own inside the
// review panel because it comes from a different store: a machine can have a
// review log and no ledger, and the panel then shows one and says so about the
// other.
type overviewQuestions struct {
	overviewSection
	Open int                   `json:"open"`
	Rows []overviewQuestionRow `json:"rows"`
}

type overviewQuestionRow struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Class string `json:"class"`
	// Score is the ledger's own attention ranking. It orders the inbox and
	// says nothing about whether an answer is true; the surface that shows
	// it says so.
	Score  int    `json:"score"`
	Prompt string `json:"prompt"`
}

// overviewRuns is the receipts an exploration left behind.
type overviewRuns struct {
	overviewSection
	Total int              `json:"total"`
	Rows  []overviewRunRow `json:"rows"`
}

type overviewRunRow struct {
	ReceiptID     string `json:"receipt_id"`
	RunID         string `json:"run_id"`
	PreparationID string `json:"preparation_id"`
	RecordedAt    string `json:"recorded_at"`
	Sync          string `json:"sync"`
	Retrievals    int    `json:"retrievals"`
	Deferred      int    `json:"deferred"`
	Failures      int    `json:"failures"`
	Redactions    int    `json:"redactions"`
	// Hypotheses is how many candidates this run put on the frontier,
	// counted from the frontier rather than from the receipt: the receipt
	// records what the run did, and the frontier records what survived it.
	Hypotheses int `json:"hypotheses"`
	// Recipes is the §5.1 provenance read from this run's observations. It
	// is empty when the run recorded no observation this server could
	// reach, which is an absence rather than a run without a recipe.
	Recipes []overviewRecipe `json:"recipes"`
	// Authority is why the run happened, as the receipt recorded it. The
	// zero value means the receipt predates the field, which the surfaces
	// that render it say rather than filling in.
	Authority RunAuthority `json:"authority"`
}

type overviewRecipe struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// overviewActivity is the catalog's most recently touched sessions, which is
// what "something happened on this machine" looks like from durable state.
type overviewActivity struct {
	overviewSection
	Rows []overviewActivityRow `json:"rows"`
}

// overviewActivityRow keeps SessionRow's nullability exactly. A session the
// scan has not described yet has no title and no modification time, and a row
// that could not say so would render an unread session as an untitled one.
type overviewActivityRow struct {
	Harness         string  `json:"harness"`
	Selector        string  `json:"selector"`
	Title           *string `json:"title"`
	TitleProvenance *string `json:"title_provenance"`
	Modified        *string `json:"modified"`
}

// handleOverview assembles the dashboard's snapshot.
//
// The order is deliberate: the frontier read produces the run-to-candidate
// index the runs section needs, so the two are assembled together rather than
// each enumerating the frontier for itself.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	var out overview
	out.Archive = s.overviewArchive(r)
	out.Corpus, out.Activity = s.overviewCatalog(r)
	var byRun map[string][]string
	out.Frontier, byRun = s.overviewFrontier(r)
	out.Runs = s.overviewRuns(r, byRun)
	out.Review = s.overviewReview(r)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) overviewArchive(r *http.Request) overviewArchive {
	section := overviewArchive{Hosts: []overviewArchiveHost{}}
	if s.opts.State != nil {
		state, err := s.opts.State.WebState(r.Context())
		if err != nil {
			s.logf("GET %s: storage state unavailable", r.URL.Path)
			section.overviewSection = sectionMissing("This machine's storage configuration could not be read.")
			return section
		}
		section.Configured, section.HostID = state.Configured, state.HostID
		if state.Configured {
			section.Repository = state.Repository
		}
	}
	if s.opts.Archive == nil || !section.Configured {
		section.overviewSection = sectionMissing(
			"No repository is configured, so there are no snapshots to report. Run `babel storage configure` to connect one.")
		return section
	}
	// The bound is on this read alone. A repository that has gone away must
	// cost the archive panel and nothing else.
	ctx, cancel := context.WithTimeout(r.Context(), overviewArchiveBudget)
	defer cancel()
	status, err := s.opts.Archive.ArchiveStatus(ctx)
	if err != nil {
		s.logf("GET %s: archive status unavailable", r.URL.Path)
		section.overviewSection = sectionMissing(
			"Snapshot status could not be read from the repository. The Archive page reports why.")
		return section
	}
	section.overviewSection = sectionReady()
	section.Snapshots, section.HostsTotal = status.Snapshots, len(status.Hosts)
	if status.Catalog != nil {
		reachable := status.Catalog.Reachable
		section.Reachable = &reachable
		section.Uncatalogued, section.Pending = status.Catalog.Uncatalogued, status.Catalog.Pending
	}
	hosts := make([]StatusHostRow, len(status.Hosts))
	copy(hosts, status.Hosts)
	// Newest first, so the panel's rows are the hosts an operator is most
	// likely to be waiting on. A host that has never published sorts last
	// rather than first: an empty time is not a recent one.
	sort.SliceStable(hosts, func(i, j int) bool {
		if (hosts[i].LatestTime == "") != (hosts[j].LatestTime == "") {
			return hosts[j].LatestTime == ""
		}
		if hosts[i].LatestTime != hosts[j].LatestTime {
			return hosts[i].LatestTime > hosts[j].LatestTime
		}
		return hosts[i].Host < hosts[j].Host
	})
	for _, host := range hosts {
		if len(section.Hosts) >= overviewRows {
			break
		}
		section.Hosts = append(section.Hosts, overviewArchiveHost{
			Host:          host.Host,
			Snapshots:     host.Snapshots,
			LatestTime:    host.LatestTime,
			LatestShortID: host.LatestShortID,
		})
	}
	if len(hosts) > 0 {
		section.LatestTime = hosts[0].LatestTime
	}
	return section
}

// overviewCatalog reads the session listing once and answers two panels from
// it. Corpus is what the catalog holds in aggregate; Activity is its newest
// rows. Two sections rather than one because they answer different questions,
// and one read rather than two because they answer them from the same catalog.
func (s *Server) overviewCatalog(r *http.Request) (overviewCorpus, overviewActivity) {
	corpus := overviewCorpus{Harnesses: []overviewHarness{}}
	activity := overviewActivity{Rows: []overviewActivityRow{}}
	if s.opts.Lister == nil {
		note := "The session catalog is not available in this session."
		corpus.overviewSection, activity.overviewSection = sectionMissing(note), sectionMissing(note)
		return corpus, activity
	}
	result, err := s.opts.Lister.ListSessions(r.Context())
	if err != nil {
		s.logf("GET %s: session catalog unavailable", r.URL.Path)
		note := "The session catalog could not be read. The Sessions page reports why."
		corpus.overviewSection, activity.overviewSection = sectionMissing(note), sectionMissing(note)
		return corpus, activity
	}
	corpus.overviewSection, activity.overviewSection = sectionReady(), sectionReady()
	corpus.RefreshedAt, corpus.Scan = result.RefreshedAt, result.Scan
	corpus.Sessions = len(result.Sessions)
	if corpus.Scan.Total > corpus.Scan.Described {
		corpus.Pending = corpus.Scan.Total - corpus.Scan.Described
	}

	byHarness := map[string]*overviewHarness{}
	order := make([]string, 0, 4)
	for _, row := range result.Sessions {
		counts, ok := byHarness[row.Harness]
		if !ok {
			counts = &overviewHarness{Harness: row.Harness}
			byHarness[row.Harness] = counts
			order = append(order, row.Harness)
		}
		counts.Sessions++
		if row.Title == nil {
			continue
		}
		corpus.Titled++
		counts.Titled++
		if row.TitleProvenance == nil {
			continue
		}
		switch *row.TitleProvenance {
		case "recorded":
			corpus.Recorded++
		case "derived":
			corpus.Derived++
		case "inferred":
			corpus.Inferred++
		}
	}
	for _, harness := range order {
		corpus.Harnesses = append(corpus.Harnesses, *byHarness[harness])
	}
	sort.SliceStable(corpus.Harnesses, func(i, j int) bool {
		if corpus.Harnesses[i].Sessions != corpus.Harnesses[j].Sessions {
			return corpus.Harnesses[i].Sessions > corpus.Harnesses[j].Sessions
		}
		return corpus.Harnesses[i].Harness < corpus.Harnesses[j].Harness
	})

	rows := make([]SessionRow, len(result.Sessions))
	copy(rows, result.Sessions)
	// Newest modification first; a session the scan has not reached has no
	// modification time and sorts last rather than pretending to be old.
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Modified, rows[j].Modified
		if (left == nil) != (right == nil) {
			return right == nil
		}
		if left != nil && *left != *right {
			return *left > *right
		}
		return rows[i].Selector < rows[j].Selector
	})
	for _, row := range rows {
		if len(activity.Rows) >= overviewRows {
			break
		}
		activity.Rows = append(activity.Rows, overviewActivityRow{
			Harness:         row.Harness,
			Selector:        row.Selector,
			Title:           row.Title,
			TitleProvenance: row.TitleProvenance,
			Modified:        row.Modified,
		})
	}
	return corpus, activity
}

// overviewFrontier counts the frontier by status and returns the run-to-
// candidate index the runs panel reads.
//
// Every candidate is read, because status is the newest entry of an
// append-only history rather than a column a count could be pushed down to —
// the same reason handleHypotheses reads records when a status filter is
// asked for. The enumeration is the shared one, so the dashboard's total is
// the Hypotheses page's total and neither can drift from `babel hypotheses`.
func (s *Server) overviewFrontier(r *http.Request) (overviewFrontier, map[string][]string) {
	statuses := []frontier.Status{
		frontier.StatusUntriaged, frontier.StatusQueued, frontier.StatusInvestigating,
		frontier.StatusDeferred, frontier.StatusRejected, frontier.StatusPromoted,
	}
	section := overviewFrontier{Statuses: []overviewStatusCount{}, Rows: []overviewHypothesis{}}
	for _, status := range statuses {
		section.Statuses = append(section.Statuses, overviewStatusCount{Status: string(status)})
	}
	if s.opts.Frontier == nil || s.opts.Review == nil {
		section.overviewSection = sectionMissing("The hypothesis frontier is not available in this session.")
		return section, nil
	}
	ids, err := s.hypothesisIDs(r.Context())
	if err != nil {
		s.logf("GET %s: frontier enumeration refused", r.URL.Path)
		section.overviewSection = sectionMissing("The hypothesis frontier could not be read.")
		return section, nil
	}
	byStatus := make(map[frontier.Status]int, len(statuses))
	byRun := make(map[string][]string, len(ids))
	records := make([]frontier.Hypothesis, 0, len(ids))
	for _, id := range ids {
		record, err := s.opts.Frontier.Hypothesis(r.Context(), id)
		if err != nil {
			s.logf("GET %s: frontier record unreadable", r.URL.Path)
			section.overviewSection = sectionMissing("The hypothesis frontier could not be read.")
			return section, nil
		}
		byStatus[record.Status]++
		byRun[record.RunID] = append(byRun[record.RunID], record.ID)
		records = append(records, record)
	}
	section.overviewSection = sectionReady()
	section.Hypotheses = len(records)
	section.Truncated = len(ids) >= listScanCap
	for i := range section.Statuses {
		section.Statuses[i].Count = byStatus[frontier.Status(section.Statuses[i].Status)]
	}
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.After(records[j].CreatedAt)
		}
		return records[i].ID < records[j].ID
	})
	for _, record := range records {
		if len(section.Rows) >= overviewRows {
			break
		}
		section.Rows = append(section.Rows, overviewHypothesis{
			ID:        record.ID,
			RunID:     record.RunID,
			Status:    string(record.Status),
			CreatedAt: timeText(record.CreatedAt),
			Statement: record.Payload.Statement,
		})
	}
	return section, byRun
}

func (s *Server) overviewRuns(r *http.Request, byRun map[string][]string) overviewRuns {
	section := overviewRuns{Rows: []overviewRunRow{}}
	if s.opts.Runs == nil {
		section.overviewSection = sectionMissing("Run receipts are not available in this session.")
		return section
	}
	summaries, total, err := s.opts.Runs.Runs(r.Context(), overviewRows, 0)
	if err != nil {
		s.logf("GET %s: run receipts refused", r.URL.Path)
		section.overviewSection = sectionMissing("Run receipts could not be read.")
		return section
	}
	section.overviewSection = sectionReady()
	section.Total = total
	for _, summary := range summaries {
		row := overviewRunRow{
			ReceiptID:     summary.ReceiptID,
			RunID:         summary.RunID,
			PreparationID: summary.PreparationID,
			RecordedAt:    summary.RecordedAt,
			Sync:          summary.Sync,
			Retrievals:    summary.Counts.Retrieval,
			Deferred:      summary.Counts.Deferred,
			Failures:      summary.Counts.Failures,
			Redactions:    summary.Counts.Redactions,
			Hypotheses:    len(byRun[summary.RunID]),
			Recipes:       s.overviewRecipes(r.Context(), summary.RunID, byRun[summary.RunID]),
			Authority:     summary.Authority,
		}
		section.Rows = append(section.Rows, row)
	}
	return section
}

// overviewRecipes reads §5.1 recipe provenance for one run from a few of its
// candidates' observations. An observation names the recipe that produced it
// and the run that recorded it, and only observations recorded by this run
// count: a refinement run's observations on the same candidate belong to that
// run's recipe, not to this one.
func (s *Server) overviewRecipes(ctx context.Context, runID string, hypotheses []string) []overviewRecipe {
	recipes := []overviewRecipe{}
	if s.opts.Frontier == nil || runID == "" {
		return recipes
	}
	seen := map[overviewRecipe]struct{}{}
	for probed, id := range hypotheses {
		if probed >= overviewRecipeProbe {
			break
		}
		observations, err := s.opts.Frontier.ObservationsFor(ctx, id)
		if err != nil {
			// The recipe is provenance a panel row annotates, so a read
			// that fails leaves the annotation absent rather than
			// failing the section that does have its counts.
			return recipes
		}
		for _, observation := range observations {
			if observation.RunID != runID || observation.RecipeID == "" {
				continue
			}
			recipe := overviewRecipe{ID: observation.RecipeID, Version: observation.RecipeVersion}
			if _, ok := seen[recipe]; ok {
				continue
			}
			seen[recipe] = struct{}{}
			recipes = append(recipes, recipe)
		}
	}
	sort.SliceStable(recipes, func(i, j int) bool {
		if recipes[i].ID != recipes[j].ID {
			return recipes[i].ID < recipes[j].ID
		}
		return recipes[i].Version < recipes[j].Version
	})
	return recipes
}

func (s *Server) overviewReview(r *http.Request) overviewReview {
	section := overviewReview{Rows: []overviewReviewRow{}}
	section.Questions = s.overviewQuestions(r)
	section.Dispositions = s.overviewDispositions(r)
	if s.opts.Review == nil || s.opts.Frontier == nil {
		section.overviewSection = sectionMissing("The review service is not available in this session.")
		return section
	}
	// The service default: records awaiting a first decision, which is what
	// an inbox is. The order is the service's own enrolment order and is not
	// re-sorted here, for the reason handleReviewQueue states.
	items, err := s.opts.Review.Queue(r.Context(), review.QueueFilter{Limit: listScanCap})
	if err != nil {
		s.logf("GET %s: review queue refused", r.URL.Path)
		section.overviewSection = sectionMissing("The review queue could not be read.")
		return section
	}
	section.overviewSection = sectionReady()
	section.Awaiting = len(items)
	for _, item := range items {
		if len(section.Rows) >= overviewRows {
			break
		}
		excerpt, err := s.excerpt(r.Context(), item.Subject)
		if err != nil {
			s.logf("GET %s: review subject unreadable", r.URL.Path)
			section.overviewSection = sectionMissing("The review queue could not be read.")
			section.Rows = []overviewReviewRow{}
			return section
		}
		section.Rows = append(section.Rows, overviewReviewRow{
			Type:       string(item.Subject.Type),
			ID:         item.Subject.ID,
			Status:     string(item.Status),
			EnrolledAt: timeText(item.EnrolledAt),
			Excerpt:    excerpt,
		})
	}
	return section
}

// overviewDispositions counts the proposed next actions nobody has answered.
//
// The count is of proposed actions rather than of the records carrying them: an
// operator answers actions one at a time, and a count of records would say a
// smaller number than the number of clicks waiting.
func (s *Server) overviewDispositions(r *http.Request) overviewDispositions {
	section := overviewDispositions{}
	if s.opts.Dispositions == nil {
		section.overviewSection = sectionMissing("Proposed next actions are not available in this session.")
		return section
	}
	// The store derives status from its ledger rather than storing it, so a
	// status filter is applied after the derivation and the total it reports
	// is what matched — which is the count this panel wants, not the length
	// of the page it would have served.
	_, total, err := s.opts.Dispositions.List(r.Context(), disposition.ListFilter{
		Statuses: []disposition.Status{disposition.StatusProposed},
		Limit:    1,
	})
	if err != nil {
		s.logf("GET %s: proposed actions refused", r.URL.Path)
		section.overviewSection = sectionMissing("Proposed next actions could not be read.")
		return section
	}
	section.overviewSection = sectionReady()
	section.Pending = total
	return section
}

func (s *Server) overviewQuestions(r *http.Request) overviewQuestions {
	section := overviewQuestions{Rows: []overviewQuestionRow{}}
	if s.opts.Reality == nil {
		section.overviewSection = sectionMissing("The reality ledger is not available in this session.")
		return section
	}
	items, err := s.opts.Reality.Inbox(r.Context(), reality.InboxQuery{Limit: listScanCap})
	if err != nil {
		s.logf("GET %s: reality inbox refused", r.URL.Path)
		section.overviewSection = sectionMissing("The question inbox could not be read.")
		return section
	}
	section.overviewSection = sectionReady()
	section.Open = len(items)
	for _, item := range items {
		if len(section.Rows) >= overviewRows {
			break
		}
		section.Rows = append(section.Rows, overviewQuestionRow{
			ID:     item.Question.ID,
			State:  string(item.Question.State),
			Class:  string(item.Question.Class),
			Score:  item.Score,
			Prompt: item.Question.Payload.Prompt,
		})
	}
	return section
}
