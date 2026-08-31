package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// The §2.7 bootstrap exchange. A launch mints one 256-bit nonce, publishes it
// only in the launch URL's fragment, and trades it — once, in a request body —
// for a session cookie the page itself cannot read. Everything after that
// authenticates by cookie.
//
// What this buys is stated in decision 34 and issue #72: the credential that
// authorizes every request is no longer reachable from JavaScript, so an XSS
// hole or a compromised frontend dependency can act as the session while the
// page is open but cannot read the session out and use it elsewhere. That is
// the difference between a bug in the page and a stolen credential.

const (
	// bootstrapPath is the one /api route that authenticates instead of
	// requiring authentication, so the middleware exempts it from the
	// credential check and from nothing else.
	bootstrapPath = "/api/bootstrap"

	// sessionCookieName carries no `__Host-` prefix because that prefix
	// requires `Secure`, which this server does not set (see
	// setSessionCookie).
	sessionCookieName = "babel_session"
)

// BootstrapTTL is how long the launch nonce stays exchangeable. §2.7 requires
// it to expire quickly, and quickly is measured against the only alternative
// the operator has: the fragment sits in a terminal's scrollback for as long as
// the window is open, and before this exchange existed the credential printed
// there stayed live for the process's whole lifetime.
//
// Two minutes rather than seconds because the nonce has to survive a cold
// browser start and an operator who pastes the URL by hand, and rather than
// hours because the printed link is the whole exposure: after it expires the
// scrollback holds a string that authorizes nothing. A launch whose link is
// stale is not a broken launch — `babel web` is one command, and the refusal
// says so.
const BootstrapTTL = 2 * time.Minute

// credentials is one launch's authentication state: the single-use bootstrap
// nonce, the instant it stops being exchangeable, and the session the exchange
// rotates it into.
//
// One mutex rather than three atomics, because consuming the nonce and issuing
// the session is one indivisible step. Two requests replaying the same launch
// URL at the same instant would otherwise both find the nonce live and both be
// handed a session, which is exactly what "single-use" denies.
type credentials struct {
	mu sync.Mutex
	// nonce is emptied by the first exchange, by expiry, and by revocation.
	// Empty means no exchange can succeed, which is why every refusal below
	// can distinguish "already used" from "never matched".
	nonce    string
	deadline time.Time
	session  string
	// revoked is the operator's lock. It is remembered rather than inferred
	// from the two empty strings above, because a locked server must refuse
	// with the reason the operator caused, not with the one a stale link
	// gets.
	revoked bool
}

// secret is 256 bits from the system CSPRNG, hex encoded.
//
// crypto/rand.Read cannot fail: since Go 1.24 it either fills the buffer
// entirely or crashes the program, so there is no error branch to write here
// and no half-random credential to defend against.
func secret() string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// exchange trades the supplied nonce for a new session, or reports why it
// would not. A non-empty refusal is what the operator is shown, so each one
// names the state that caused it and the one command that fixes it.
//
// A nonce that does not match consumes nothing. The launch link is 256 bits, so
// guessing it is not the threat; a local process that could invalidate a live
// launch by posting rubbish at it would be.
func (c *credentials) exchange(supplied string, now time.Time) (session, refusal string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.revoked:
		return "", "this server was locked by the operator; run `babel web` again for a new link"
	case c.nonce == "":
		return "", "this launch link was already used; run `babel web` again for a new link"
	case now.After(c.deadline):
		// The nonce is dropped here rather than merely failing this
		// check, so an expiry cannot be undone by a clock that moves
		// backwards.
		c.nonce = ""
		return "", "this launch link expired after " + BootstrapTTL.String() + "; run `babel web` again for a new link"
	case subtle.ConstantTimeCompare([]byte(supplied), []byte(c.nonce)) != 1:
		return "", "unauthorized"
	}
	c.nonce = ""
	// Rotated, not reused: the session is independently generated, so the
	// credential the page holds is not the one that was printed to a
	// terminal and left in a scrollback.
	c.session = secret()
	return c.session, ""
}

// authorize reports whether the presented cookie value is this launch's live
// session. An empty session is never authorized, which is what keeps a request
// carrying an empty cookie — or the nonce, before any exchange — from matching
// a zero value.
func (c *credentials) authorize(presented string) bool {
	if presented == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked || c.session == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(c.session)) == 1
}

// revoke is lock and stop's first half (§2.7: "lock/stop revokes every nonce
// and session"). Both credentials die together and nothing can be exchanged
// afterwards, so a tab the operator left open and a launch link never opened
// are equally worthless.
func (c *credentials) revoke() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = true
	c.nonce = ""
	c.session = ""
}

// bootstrapRequest is the exchange's body. The nonce travels in a body rather
// than a header or a query parameter because a body reaches no request line,
// no access log, and no Referer (§2.7).
type bootstrapRequest struct {
	Nonce string `json:"nonce"`
}

// bootstrapResult confirms the exchange without echoing anything. The client
// has no use for the session value and could not read the cookie that carries
// it, which is the point.
type bootstrapResult struct {
	Established bool `json:"established"`
}

// handleBootstrap performs the §2.7 exchange: POST the launch nonce, receive a
// rotated host-only `HttpOnly; SameSite=Strict` session cookie.
//
// It is reached through the same origin guard as every other /api route and
// skips only the credential check, because it is the credential check's own
// source. A missing or malformed body is a client mistake and reported as one;
// every refusal that concerns the nonce is 401, which is what the page renders
// as its unauthorized state.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var supplied bootstrapRequest
	// The whole document is one 64-character hex string in one field. A body
	// larger than a kilobyte is not a bootstrap, and reading it would be
	// work owed to a request that is about to be refused.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&supplied); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed bootstrap request")
		return
	}
	if supplied.Nonce == "" {
		s.writeError(w, http.StatusBadRequest, "no bootstrap nonce")
		return
	}
	session, refusal := s.creds.exchange(supplied.Nonce, time.Now())
	if refusal != "" {
		s.logf("bootstrap refused: %s", refusal)
		s.writeError(w, http.StatusUnauthorized, refusal)
		return
	}
	setSessionCookie(w, session)
	s.logf("bootstrap exchanged: launch nonce consumed, session established")
	s.writeJSON(w, http.StatusOK, bootstrapResult{Established: true})
}

// setSessionCookie writes the session credential the page cannot read.
//
// HttpOnly is the whole reason this exchange exists: script in the page can
// send the cookie by making a request, and cannot read it to send anywhere
// else. SameSite=Strict keeps it off every cross-site request, so a page the
// operator merely visited cannot drive this API even with the cookie present in
// the browser. No Domain attribute makes it host-only, so it is never widened
// to a parent domain. No Expires or Max-Age makes it a session cookie, which
// dies with the browser; the server-side session dies with the process or with
// lock/stop, whichever comes first.
//
// Secure is deliberately absent, and it is the one attribute §2.7's wording
// does not settle. The listener binds 127.0.0.1, so there is no network path
// between browser and server for a downgrade to attack, and `Secure` is defined
// against the scheme rather than the destination: browsers disagree about
// whether a `http://127.0.0.1` origin may set one — Chromium treats loopback as
// potentially trustworthy and accepts it, others have historically dropped it —
// so setting it would make the session work on some browsers and silently fail
// on others while defending against nothing. The same reasoning rules out the
// `__Host-` prefix, which requires Secure.
//
// What that costs is recorded rather than glossed: cookies are not isolated by
// port, so another server on some other loopback port could overwrite this
// cookie in the operator's browser. It cannot forge a valid value, so the
// consequence is that the operator's page starts getting 401s and they relaunch
// — a nuisance, not a compromise — and no cookie attribute available to an
// `http://` loopback origin prevents it.
func setSessionCookie(w http.ResponseWriter, session string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
