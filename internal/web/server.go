// Package web provides Babel's authenticated loopback management server.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const defaultTranscriptLimit = 200

// Server is a single-use authenticated HTTP server bound to the IPv4
// loopback interface.
type Server struct {
	opts     Options
	listener net.Listener
	// creds is the launch's §2.7 authentication state: the single-use
	// bootstrap nonce and the session cookie it is exchanged for. It is a
	// value rather than a pointer because a Server is never copied, and it
	// owns its own mutex because the exchange is a read-modify-write.
	creds    credentials
	url      string
	logMu    sync.Mutex
	lockOnce sync.Once
	locked   chan struct{}
}

// New mints the launch nonce and binds a loopback listener. Port zero asks the
// kernel for an available port. The listener is held until Serve begins so URL
// is immediately stable and safe to launch.
//
// The nonce is placed in the URL fragment, which browsers never transmit
// (SPEC.md §148), so it reaches neither the request line nor anything that
// records one. It authorizes exactly one thing — the bootstrap exchange — and
// it expires after BootstrapTTL.
func New(opts Options) (*Server, error) {
	if opts.Port < 0 || opts.Port > 65535 {
		return nil, fmt.Errorf("port %d is out of range", opts.Port)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port)))
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	nonce := secret()
	port := listener.Addr().(*net.TCPAddr).Port
	return &Server{
		opts:     opts,
		listener: listener,
		creds:    credentials{nonce: nonce, deadline: time.Now().Add(BootstrapTTL)},
		url:      fmt.Sprintf("http://127.0.0.1:%d/#nonce=%s", port, nonce),
		locked:   make(chan struct{}),
	}, nil
}

// URL is the launch URL. The nonce rides in the fragment, so opening this URL
// sends no credential to the server; the bootstrap code reads it once, posts it
// in a body, and holds the session cookie the exchange returns.
func (s *Server) URL() string { return s.url }

// Serve handles requests until ctx is canceled, the operator locks the server,
// or the server fails. A Server is single-use because New reserves exactly one
// listener.
//
// A lock returns nil: stopping is what the operator asked for, so `babel web`
// exits successfully rather than reporting the shutdown it was told to perform
// as a failure.
func (s *Server) Serve(ctx context.Context) error {
	httpServer := &http.Server{
		Handler:           s.middleware(http.HandlerFunc(s.route)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.Serve(s.listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-s.locked:
		return s.shutdown(httpServer, serveResult)
	case <-ctx.Done():
		return s.shutdown(httpServer, serveResult)
	}
}

// shutdown stops accepting connections and drains the ones already accepted.
// The lock handler is still in flight when it arrives here, so the graceful
// wait is what puts its confirmation on the wire.
func (s *Server) shutdown(httpServer *http.Server, serveResult <-chan error) error {
	// Closing the listener first also covers cancellation racing with
	// http.Server's listener registration.
	_ = s.listener.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	serveErr := <-serveResult
	// Shutdown closes the listeners it tracks, so it closes the one already
	// closed above and reports that as its error - unless Serve returned
	// first and untracked it. Which of those happens is scheduling, so the
	// error means nothing about whether the shutdown worked: an already
	// closed listener is the state being asked for. Under load the loser
	// changes, which is how an operator-requested lock could exit non-zero
	// on a server that stopped exactly as told.
	if shutdownErr != nil && !errors.Is(shutdownErr, net.ErrClosed) {
		return shutdownErr
	}
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		return nil
	}
	return serveErr
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logf("panic serving %s %s: %v", r.Method, r.URL.Path, recovered)
				if !rw.wroteHeader {
					s.writeError(rw, http.StatusInternalServerError, "internal server error")
				}
			}
			s.logf("%s %s %d %s", r.Method, r.URL.Path, rw.statusCode(), time.Since(start).Round(time.Millisecond))
		}()

		rw.Header().Set("Content-Security-Policy", "default-src 'self'")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api") {
			rw.Header().Set("Cache-Control", "no-store")
			if !s.sameOrigin(r) {
				s.writeError(rw, http.StatusForbidden, "forbidden origin")
				return
			}
			// The bootstrap exchange is the one /api route that
			// authenticates rather than requiring authentication,
			// so it is exempt from this check and from nothing
			// else: it still passes the origin guard above, still
			// answers `no-store`, and still refuses a locked
			// server.
			if r.URL.Path != bootstrapPath && !s.authorized(r) {
				s.writeError(rw, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next.ServeHTTP(rw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap exposes the real writer so http.ResponseController can still reach
// its Flush through this wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// sameOrigin is the shared guard every /api request passes before its
// credential is even examined. Two weak signals, checked rather than trusted
// to stay unreachable, because /api/lock turns a forged request into a denial
// of service:
//
// Host must name the loopback literal this server binds. A request arriving
// here under any other name was aimed at a hostname that resolves to
// 127.0.0.1, which is DNS rebinding.
//
// Origin, when present, must be this server's own.
//
// A missing Origin is still allowed, and the cookie exchange is what makes
// that safe to keep rather than merely inherited. The session cookie is
// SameSite=Strict, so no cross-site request carries it at all; a browser that
// omits Origin is making a same-origin GET or navigation, and every mutation
// on this surface is a POST, which browsers always send an Origin with. The
// one attacker the SameSite attribute does not stop is a page on another
// loopback *port* — cookies are not isolated by port — and that page's POST
// does carry an Origin, naming its own port, which this refuses. A non-browser
// client can of course send any Origin it likes; it holds no session cookie,
// which is the defence that actually decides the request.
func (s *Server) sameOrigin(r *http.Request) bool {
	if !loopbackHost(r.Host) {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host
}

func loopbackHost(host string) bool {
	hostname := host
	if split, _, err := net.SplitHostPort(host); err == nil {
		hostname = split
	}
	return hostname == "127.0.0.1" || hostname == "localhost"
}

// authorized accepts the session established by the §2.7 exchange, from its
// cookie and from nowhere else.
//
// There is no header, query-parameter or body credential to accept. The
// bootstrap nonce is not one either: it authorizes the exchange and is spent by
// it, so a request presenting the nonce as a session is refused like any other
// wrong value. A credential this narrow is what makes the guarantee checkable —
// the live credential is unreadable from JavaScript, and there is no second
// channel a future change could quietly widen it through.
func (s *Server) authorized(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	// A locked server has no live session at all, which credentials.authorize
	// enforces: a request that raced the lock, or a retry from a tab the
	// operator left open, is refused by a process that is still answering
	// only because it has not finished stopping.
	return s.creds.authorize(cookie.Value)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") {
		s.routeAPI(w, r)
		return
	}
	s.serveStatic(w, r)
}

func (s *Server) routeAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	// The §2.7 bootstrap exchange comes first because it is what every other
	// case below requires: no session exists until this one runs.
	case bootstrapPath:
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleBootstrap(w, r)
	case "/api/version":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.writeJSON(w, http.StatusOK, readVersion())
	case "/api/state":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleState(w, r)
	// The dashboard's aggregate read. It spans both generations of routes,
	// which is why it is here rather than with either: it reads the archive,
	// the catalog, the frontier, the review log, the ledger and the receipts
	// through the same interfaces their own routes use, and reports each
	// section's availability instead of refusing the whole document when one
	// store is missing.
	case "/api/overview":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleOverview(w, r)
	case "/api/sessions":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleSessions(w, r)
	case "/api/scan":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleScan(w)
	case "/api/sessions/refresh":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleSessionsRefresh(w)
	case "/api/session":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleSession(w, r)
	case "/api/transcript":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleTranscript(w, r)
	case "/api/archive/status":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleArchiveStatus(w, r)
	case "/api/archive/sessions":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleArchiveSessions(w, r)
	case "/api/archive/verify":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleArchiveVerify(w, r)
	case "/api/fetch":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleFetch(w, r)
	// The Phase B analysis, review, and Reality surface. Every one of these
	// paths reaches the same middleware the Phase A routes do — the
	// loopback Host check, the Origin check, the launch session, and
	// `no-store` — because they are under /api and there is one guard, not
	// one per generation of routes. There is deliberately no route that
	// starts an exploration; analysis.go's exploreRefusal says why, and
	// GET /api/analysis/state reports it.
	case "/api/analysis/state":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleAnalysisState(w, r)
	case "/api/hypotheses":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleHypotheses(w, r)
	case "/api/hypothesis":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleHypothesis(w, r)
	case "/api/findings":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleFindings(w, r)
	case "/api/finding":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleFinding(w, r)
	case "/api/review/queue":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleReviewQueue(w, r)
	case "/api/review/decide":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleReviewDecide(w, r)
	case "/api/review/context":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleReviewContext(w, r)
	case "/api/review/history":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleReviewHistory(w, r)
	case "/api/export":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleExport(w, r)
	case "/api/reality/inbox":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleRealityInbox(w, r)
	case "/api/reality/entity":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleRealityEntity(w, r)
	case "/api/reality/answer":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleRealityAnswer(w, r)
	case "/api/reality/plan/accept":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleRealityPlanAccept(w, r)
	// Issue #87's record actions. The three POSTs are the browser's first
	// writes against the frontier's own state, and each carries the chain
	// head the page was rendered against; internal/web/records.go states
	// why.
	case "/api/record/revisions":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleRecordRevisions(w, r)
	case "/api/record/dispositions":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleRecordDispositions(w, r)
	// Issue #113's reference graph read. It sits with #87's record routes
	// because it is the third read one record page performs — the chain, the
	// proposed actions, and now the citations — and it takes the same
	// ?type=&id= pair the other two do so a page names its subject once.
	case "/api/record/links":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleRecordLinks(w, r)
	case "/api/record/disposition/decide":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleDecideDisposition(w, r)
	case "/api/record/invite":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleInviteRecord(w, r)
	case "/api/record/revive":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleReviveRecord(w, r)
	// Issue #109's fleet read. Both are GETs and neither reaches a writer:
	// internal/fleet holds no publisher and this surface holds no ingest, so
	// the browser can see what every host committed and can change none of
	// it. internal/web/fleet.go states why an unconfigured fleet answers 200
	// with `configured: false` rather than refusing.
	case "/api/fleet/records":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleFleetRecords(w, r)
	case "/api/fleet/hosts":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleFleetHosts(w, r)
	case "/api/search":
		if !s.requireMethod(w, r, http.MethodGet) {
			return
		}
		s.handleSearch(w, r)
	case "/api/lock":
		if !s.requireMethod(w, r, http.MethodPost) {
			return
		}
		s.handleLock(w)
	default:
		s.writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	s.writeError(w, http.StatusBadRequest, "unsupported method")
	return false
}

// handleState reports the non-secret storage configuration.
//
// An unconfigured server blanks the repository, because there is none to name.
// It does not blank the host id: that is this machine's own identity, resolved
// from the launch flag, the environment, storage.json, or the system hostname,
// and it has an answer whether or not a repository exists. The Sessions page
// needs it to say whose sessions it is listing, which is a question a missing
// repository does not make unanswerable.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if s.opts.State == nil {
		s.writeJSON(w, http.StatusOK, State{})
		return
	}
	state, err := s.opts.State.WebState(r.Context())
	if err != nil {
		s.operationError(w, err)
		return
	}
	if !state.Configured {
		state.Repository = ""
	}
	s.writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.opts.Lister == nil {
		s.writeError(w, http.StatusInternalServerError, "session lister unavailable")
		return
	}
	result, err := s.opts.Lister.ListSessions(r.Context())
	if err != nil {
		s.operationError(w, err)
		return
	}
	if result.Sessions == nil {
		result.Sessions = []SessionRow{}
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleScan is the cheap poll target: it reports the scan's progress
// without touching the catalog, so a client can follow a long scan without
// re-serializing the whole listing.
func (s *Server) handleScan(w http.ResponseWriter) {
	if s.opts.Scanner == nil {
		s.writeError(w, http.StatusInternalServerError, "session scanner unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, s.opts.Scanner.State())
}

// handleSessionsRefresh starts a scan when none is running and attaches to
// the running one otherwise, so a client that asks twice gets one scan.
func (s *Server) handleSessionsRefresh(w http.ResponseWriter) {
	if s.opts.Scanner == nil {
		s.writeError(w, http.StatusInternalServerError, "session scanner unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, s.opts.Scanner.StartRefresh())
}

func (s *Server) selector(w http.ResponseWriter, r *http.Request) (string, bool) {
	selector := r.URL.Query().Get("selector")
	if selector == "" {
		s.writeError(w, http.StatusBadRequest, "selector is required")
		return "", false
	}
	return selector, true
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	selector, ok := s.selector(w, r)
	if !ok {
		return
	}
	if s.opts.Inspector == nil {
		s.writeError(w, http.StatusInternalServerError, "session inspector unavailable")
		return
	}
	result, err := s.opts.Inspector.InspectSession(r.Context(), selector)
	if err != nil {
		s.operationError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	selector, ok := s.selector(w, r)
	if !ok {
		return
	}
	offset, ok := queryInt(r, "offset", 0)
	if !ok || offset < 0 {
		s.writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return
	}
	limit, ok := queryInt(r, "limit", defaultTranscriptLimit)
	if !ok || limit < 0 {
		s.writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return
	}
	if s.opts.Inspector == nil || s.opts.Transcripts == nil {
		s.writeError(w, http.StatusInternalServerError, "transcript reader unavailable")
		return
	}
	inspected, err := s.opts.Inspector.InspectSession(r.Context(), selector)
	if err != nil {
		s.operationError(w, err)
		return
	}
	total, events, err := s.opts.Transcripts.Events(inspected.PrimaryPath, inspected.Harness, offset, limit)
	if err != nil {
		s.operationError(w, err)
		return
	}
	responseEvents := any(events)
	if events == nil {
		responseEvents = []any{}
	}
	s.writeJSON(w, http.StatusOK, struct {
		Total  int `json:"total"`
		Events any `json:"events"`
	}{Total: total, Events: responseEvents})
}

func queryInt(r *http.Request, key string, fallback int) (int, bool) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(value)
	return n, err == nil
}

func (s *Server) handleArchiveStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Archive == nil {
		s.writeError(w, http.StatusConflict, "repository is not configured")
		return
	}
	result, err := s.opts.Archive.ArchiveStatus(r.Context())
	if err != nil {
		s.operationError(w, err)
		return
	}
	if result.Hosts == nil {
		result.Hosts = []StatusHostRow{}
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleArchiveSessions lists one host's archived sessions, which is how an
// instance browses a machine other than its own (SPEC.md §6.2).
//
// It is a route of its own rather than a `host` parameter on /api/sessions
// because the two answer different questions from different sources with
// different costs. /api/sessions serves whatever the local catalog already
// holds — immediately, from memory, with a background scan's progress
// attached — and cannot fail for repository reasons. This reads a snapshot's
// file listing out of the repository: it needs a configured repository, takes
// seconds against a remote one, has no catalog refresh time and no scan, and
// carries a snapshot's worth of rows that describe files this machine does not
// have. Folding the two into one path would put a repository round trip behind
// the endpoint a page polls, and would make the response shape a union whose
// meaningful half depends on a query parameter.
//
// The host is required and not defaulted to this machine's own identity: the
// operator's own sessions are what /api/sessions already shows, so a defaulted
// host would answer a question nobody asked with an expensive repository read.
func (s *Server) handleArchiveSessions(w http.ResponseWriter, r *http.Request) {
	if s.opts.Archive == nil {
		s.writeError(w, http.StatusConflict, "repository is not configured")
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		s.writeError(w, http.StatusBadRequest, "host is required")
		return
	}
	result, err := s.opts.Archive.ArchiveSessions(r.Context(), host, r.URL.Query().Get("snapshot"))
	if err != nil {
		s.operationError(w, err)
		return
	}
	if result.Sessions == nil {
		result.Sessions = []ArchiveSessionRow{}
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleArchiveVerify(w http.ResponseWriter, r *http.Request) {
	if s.opts.Archive == nil {
		s.writeError(w, http.StatusConflict, "repository is not configured")
		return
	}
	deepValue := r.URL.Query().Get("deep")
	if deepValue == "" {
		deepValue = "0"
	}
	if deepValue != "0" && deepValue != "1" {
		s.writeError(w, http.StatusBadRequest, "deep must be 0 or 1")
		return
	}
	result, err := s.opts.Archive.ArchiveVerify(r.Context(), deepValue == "1")
	if err != nil {
		s.operationError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleFetch materializes one archived session's file closure locally.
//
// The optional host parameter is what makes a selector discovered through
// /api/archive/sessions actionable: without it the selector is resolved
// against this machine's live source files, which by definition do not hold a
// session that only another host archived. It is the same `--host` the CLI
// takes and reaches the same command, so the browser gains no resolution the
// terminal does not already have.
func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	if s.opts.Archive == nil {
		s.writeError(w, http.StatusConflict, "repository is not configured")
		return
	}
	selector, ok := s.selector(w, r)
	if !ok {
		return
	}
	result, err := s.opts.Archive.FetchSession(r.Context(), FetchRequest{
		Selector: selector,
		Snapshot: r.URL.Query().Get("snapshot"),
		Host:     r.URL.Query().Get("host"),
	})
	if err != nil {
		s.operationError(w, err)
		return
	}
	if result.Included == nil {
		result.Included = []string{}
	}
	s.writeJSON(w, http.StatusOK, result)
}

// lockResult reports what the operator's stop request actually did. Both
// fields are stated because they are two distinct guarantees, and a page that
// is about to lose its server should not have to infer either one: revoked
// says the launch session is dead, stopping says the listener is going away.
type lockResult struct {
	Revoked  bool `json:"revoked"`
	Stopping bool `json:"stopping"`
}

// handleLock is the operator's explicit lock and stop control (SPEC.md §2,
// §8.2, decisions 34 and 45).
//
// The order is the contract. Revoking first means there is no window in which
// the process still honours the session while it winds down; only then is the
// listener asked to go away. Because the session is dead by the time this
// responds, the confirmation cannot be re-fetched, so it is flushed here
// rather than left to the graceful drain. A flush that could not happen is
// reported rather than swallowed: it would mean the wrapper stopped exposing
// the underlying writer, and the operator's only report of the outcome would
// then depend entirely on the drain.
//
// The diagnostic is emitted before the shutdown signal, not after. Flushing
// releases the client while this handler is still running, so anything left
// after the signal races every observer that the response just unblocked -
// including a caller that reads the diagnostics stream expecting the decision
// to be recorded in it. Closing `locked` last makes that channel a barrier for
// the whole handler rather than for part of it.
func (s *Server) handleLock(w http.ResponseWriter) {
	s.creds.revoke()
	s.writeJSON(w, http.StatusOK, lockResult{Revoked: true, Stopping: true})
	if err := http.NewResponseController(w).Flush(); err != nil {
		s.logf("lock confirmation was not flushed: %v", err)
	}
	s.logf("locked by operator: launch nonce and session revoked, stopping listener")
	// Serve owns the shutdown; signalling it keeps this handler off the path
	// that closes the connection it is still writing to. Once, because a
	// second lock must not close a closed channel.
	s.lockOnce.Do(func() { close(s.locked) })
}

func (s *Server) operationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadRequest):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrConflict):
		s.writeError(w, http.StatusConflict, err.Error())
	default:
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeError(w, http.StatusBadRequest, "unsupported method")
		return
	}
	if s.opts.Static == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	info, err := fs.Stat(s.opts.Static, name)
	if err != nil || info.IsDir() {
		name = "index.html"
		info, err = fs.Stat(s.opts.Static, name)
	}
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	contents, err := fs.ReadFile(s.opts.Static, name)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(contents))
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	safe, err := sanitizedJSON(value)
	if err != nil {
		status = http.StatusInternalServerError
		safe = map[string]any{"error": "encode response"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(safe)
}

func sanitizedJSON(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return sanitizeValue(decoded), nil
}

func sanitizeValue(value any) any {
	switch value := value.(type) {
	case string:
		return sanitize(value)
	case []any:
		for i := range value {
			value[i] = sanitizeValue(value[i])
		}
	case map[string]any:
		for key, entry := range value {
			value[key] = sanitizeValue(entry)
		}
	}
	return value
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Diagnostics == nil {
		return
	}
	line := sanitize(fmt.Sprintf(format, args...))
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_, _ = io.WriteString(s.opts.Diagnostics, line+"\n")
}

type versionResult struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Dirty    bool   `json:"dirty"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func readVersion() versionResult {
	result := versionResult{Version: "devel", Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return result
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		result.Version = info.Main.Version
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			result.Commit = setting.Value
		case "vcs.modified":
			result.Dirty = setting.Value == "true"
		}
	}
	return result
}

func sanitize(value string) string {
	if !mayNeedEscape(value) {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) + 8)
	for i := 0; i < len(value); {
		if c := value[i]; c < utf8.RuneSelf {
			if c < 0x20 || c == 0x7f {
				writeEscape(&builder, "\\u{", uint32(c), 1)
			} else {
				builder.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			writeEscape(&builder, "\\x{", uint32(value[i]), 2)
			i++
			continue
		}
		if unsafeRune(r) {
			writeEscape(&builder, "\\u{", uint32(r), 1)
		} else {
			builder.WriteString(value[i : i+size])
		}
		i += size
	}
	return builder.String()
}

func mayNeedEscape(value string) bool {
	for i := range len(value) {
		if value[i] < 0x20 || value[i] >= 0x7f {
			return true
		}
	}
	return false
}

func unsafeRune(r rune) bool {
	return r >= 0x80 && r <= 0x9f ||
		r >= 0x200b && r <= 0x200f ||
		r == 0x2028 || r == 0x2029 ||
		r >= 0x202a && r <= 0x202e ||
		r >= 0x2066 && r <= 0x2069 ||
		r == 0xfeff
}

const hexDigits = "0123456789ABCDEF"

func writeEscape(builder *strings.Builder, prefix string, value uint32, minDigits int) {
	builder.WriteString(prefix)
	var buf [8]byte
	n := 0
	for shift := 28; shift >= 0; shift -= 4 {
		digit := (value >> uint(shift)) & 0xf
		if digit == 0 && n == 0 && shift > 0 {
			continue
		}
		buf[n] = hexDigits[digit]
		n++
	}
	for pad := n; pad < minDigits; pad++ {
		builder.WriteByte('0')
	}
	builder.Write(buf[:n])
	builder.WriteByte('}')
}
