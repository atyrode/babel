import type { MouseEvent } from "react";
import { Link } from "react-router-dom";
import { Badge, statusTone, reviewTone } from "../analysis";

// The concepts page. It is documentation rather than a view: nothing here reads
// the API, so it renders identically with no archive, no analysis state and no
// network. That is deliberate — the page an operator reaches when
// nothing works must not depend on anything working.
//
// The vocabulary is Babel's own, and the framing is the one every analytical
// surface carries: the output is creative, fallible and incomplete, and this
// page says so first rather than in a footnote.

const STATUSES: Array<[string, string]> = [
  ["untriaged", "Recorded and not yet sorted. Every candidate starts here."],
  ["queued", "Selected for investigation, not started."],
  ["investigating", "A run is developing it, or has developed it."],
  ["deferred", "Set aside deliberately. Kept, not deleted."],
  ["rejected", "Judged not worth pursuing. Still listed, visibly rejected."],
  ["promoted", "Developed into a finding or a proposal."],
];

const REVIEW_STATUSES: Array<[string, string]> = [
  ["new", "Enrolled and awaiting a first decision."],
  ["accepted", "A reviewer accepted it, with attribution and a timestamp."],
  ["rejected", "A reviewer rejected it. The record stays."],
  ["deferred", "A reviewer postponed it."],
  ["duplicate", "A reviewer pointed it at another record."],
  ["refine-requested", "A reviewer asked for another pass."],
];

const COMMANDS: Array<[string, string, string, string]> = [
  ["babel web", "Serves this interface on loopback behind a one-time launch link.", "/", "Home"],
  ["babel storage configure", "Stores the repository and catalog configuration.", "/settings?section=archive", "Settings › Archive"],
  ["babel archive push", "Backs this host's sessions into the restic repository.", "/settings?section=archive", "Settings › Archive"],
  ["babel archive status", "Reports snapshots per host and the catalog's lag.", "/settings?section=archive", "Settings › Archive"],
  ["babel archive verify", "Checks repository integrity, standard or deep.", "/settings?section=archive", "Settings › Archive"],
  ["babel sessions list", "Lists the session transcripts the harnesses have written.", "/sessions", "Sessions"],
  ["babel sessions inspect", "Shows one session whole, with its transcript.", "/sessions", "Sessions"],
  ["babel sessions fetch", "Restores one archived session's files locally.", "/sessions", "Sessions"],
  ["babel prepare", "Fixes an exploration's corpus scope and builds its index.", "/watch", "Watch"],
  ["babel explore", "Runs one exploration through the Code worker.", "/watch", "Watch"],
  ["babel hypotheses", "Lists the candidate frontier.", "/?kind=hypothesis&needs=all", "Home"],
  ["babel findings", "Lists consolidated findings.", "/?kind=finding&needs=all", "Home"],
  ["babel review queue", "Lists records awaiting a human decision.", "/", "Home"],
  ["babel review decide", "Appends one attributed decision.", "/", "Home"],
  ["babel reality inbox", "Lists the prioritized question inbox.", "/?kind=question", "Home"],
  ["babel reality answer", "Records an attributed answer, verbatim.", "/ask/questions", "Asked"],
  ["babel conductor status", "Reports this host's duty state and what other machines announced.", "/watch", "Watch"],
  ["babel export", "Renders one record as JSON or Markdown.", "/", "Home"],
];

// The "Am I talking to an AI?" table. One row per surface an operator actually
// touches, and the middle column deliberately has only two values: nothing in
// Babel is a conversation with a model. "Only during a run" is as close as it
// gets, and both rows carrying it name the command that starts the run.
const AI_SURFACES: Array<{
  surface: string;
  mono: boolean;
  answer: "never" | "only during a run";
  reason: string;
}> = [
  {
    surface: "Web pages — every route",
    mono: false,
    answer: "never",
    reason:
      "Pages read records Babel already wrote. They can quote what a model once wrote — framed " +
      "and labeled — but nothing you do in a browser reaches a model.",
  },
  {
    surface: "babel archive push · status · verify",
    mono: true,
    answer: "never",
    reason:
      "restic plus catalog arithmetic. The hourly timer runs push unattended; still no model.",
  },
  {
    surface: "babel sessions list · inspect · fetch",
    mono: true,
    answer: "never",
    reason:
      "They read harness files and the archive as they are. Fetch restores bytes; nothing " +
      "interprets them.",
  },
  {
    surface: "babel prepare",
    mono: true,
    answer: "never",
    reason:
      "Fixes an exploration's scope and builds its retrieval index — offline computation. " +
      "Preparation never infers.",
  },
  {
    surface: "babel explore · Watch › Ask for a run",
    mono: false,
    answer: "only during a run",
    reason:
      "Both start the one sandboxed Code worker — the browser by running this machine's own " +
      "binary with the flags the CLI takes, never a run of its own assembly. When the run ends " +
      "the worker is terminated, and no agent exists anywhere.",
  },
  {
    surface: "Titles — recorded · derived",
    mono: false,
    answer: "never",
    reason:
      "The harness recorded it, or Babel computed it offline from the session's own records.",
  },
  {
    surface: "Titles — inferred",
    mono: false,
    answer: "only during a run",
    reason:
      "The model is chosen once through babel titles configure, on the operator's own terminal; " +
      "babel sessions title infer then previews exactly what would be sent to the model and runs " +
      "only on --confirm. The value is labeled inferred wherever it shows.",
  },
];

// In-page cross-links. The router owns the URL fragment (this text is a
// section of #/settings, not a route of its own), so a native #anchor link
// would be read as navigation to a route that does not exist. The href stays
// the section's own URL — which keeps middle-click and the link-audit test
// honest — and the click scrolls instead.
function scrollTo(id: string) {
  return (event: MouseEvent<HTMLAnchorElement>) => {
    event.preventDefault();
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  };
}

// The two-loops diagram, drawn as static inline SVG rather than with a chart
// library: this page must render with no network and no data, and the diagram
// is a fixed statement about the system, not a visualization of state. Each
// SVG carries role="img" with a linked title and description, so a screen
// reader gets the same claim the picture makes.
function ArchiveLoopDiagram() {
  return (
    <svg
      className="runtime-diagram"
      viewBox="0 0 360 300"
      role="img"
      aria-labelledby="rt-loop-title rt-loop-desc"
    >
      <title id="rt-loop-title">The archive loop: automatic, and no AI anywhere</title>
      <desc id="rt-loop-desc">
        An hourly timer runs babel archive push, which writes the encrypted restic repository and
        the session catalog. Web pages and CLI browsing only read what the loop wrote. No model is
        involved at any point in this loop.
      </desc>
      <defs>
        <marker id="rt-arrow-loop" markerWidth="7" markerHeight="7" refX="5.4" refY="3" orient="auto">
          <path className="runtime-arrowhead" d="M0 0L6 3L0 6Z" />
        </marker>
      </defs>
      <rect className="rt-box" x="14" y="34" width="150" height="52" rx="9" />
      <text className="rt-title" x="89" y="57" textAnchor="middle">Hourly timer</text>
      <text className="rt-sub" x="89" y="74" textAnchor="middle">systemd user timer</text>
      <rect className="rt-box" x="196" y="34" width="150" height="52" rx="9" />
      <text className="rt-title rt-mono" x="271" y="57" textAnchor="middle">babel archive push</text>
      <text className="rt-sub" x="271" y="74" textAnchor="middle">backs up session files</text>
      <path className="rt-flow" d="M164 60H190" markerEnd="url(#rt-arrow-loop)" />
      {/* The hourly return arc is two segments with a deliberate gap: the
          label sits in the gap rather than being struck through by the arc. */}
      <path className="rt-flow rt-flow--again" d="M271 32C271 14 242 9 222 9" />
      <path className="rt-flow rt-flow--again" d="M138 9C104 9 89 14 89 26" markerEnd="url(#rt-arrow-loop)" />
      <text className="rt-label" x="180" y="12" textAnchor="middle">again next hour</text>
      <path className="rt-flow" d="M271 86V112" markerEnd="url(#rt-arrow-loop)" />
      <text className="rt-label" x="279" y="104">writes</text>
      <rect className="rt-box rt-box--records" x="14" y="118" width="332" height="66" rx="9" />
      <text className="rt-title" x="180" y="142" textAnchor="middle">Babel's records</text>
      <text className="rt-sub" x="180" y="160" textAnchor="middle">
        restic repository (encrypted) · session catalog
      </text>
      <path className="rt-flow" d="M180 244V190" markerEnd="url(#rt-arrow-loop)" />
      <text className="rt-label" x="188" y="220">read — never written back</text>
      <rect className="rt-box" x="14" y="248" width="332" height="48" rx="9" />
      <text className="rt-title" x="180" y="268" textAnchor="middle">Web pages · CLI browsing</text>
      <text className="rt-sub" x="180" y="284" textAnchor="middle">
        show what the loop already wrote — and never write it back
      </text>
    </svg>
  );
}

function ExploreRunDiagram() {
  return (
    <svg
      className="runtime-diagram"
      viewBox="0 0 360 520"
      role="img"
      aria-labelledby="rt-run-title rt-run-desc"
    >
      <title id="rt-run-title">An exploration run: the only place a model runs</title>
      <desc id="rt-run-desc">
        The operator starts a run, from a terminal with babel explore or from Watch, which runs
        that same command. Inside the AI boundary, exactly one sandboxed Code worker runs under
        the profile fixed by the operator's terminal ceremony. Its evidence requests cross the
        boundary to Babel's broker, which grants or denies each one and receipts every decision.
        Everything the run produces lands in Babel's own records: the hypothesis frontier and the
        run receipt. The worker is terminated when the run ends; between runs, no agent exists.
      </desc>
      <defs>
        <marker id="rt-arrow-run" markerWidth="7" markerHeight="7" refX="5.4" refY="3" orient="auto">
          <path className="runtime-arrowhead" d="M0 0L6 3L0 6Z" />
        </marker>
        <marker id="rt-arrow-run-x" markerWidth="7" markerHeight="7" refX="5.4" refY="3" orient="auto">
          <path className="runtime-arrowhead runtime-arrowhead--exchange" d="M0 0L6 3L0 6Z" />
        </marker>
      </defs>
      <rect className="rt-box" x="14" y="14" width="332" height="52" rx="9" />
      <text className="rt-title" x="180" y="36" textAnchor="middle">You — a terminal, or Watch</text>
      <text className="rt-sub rt-mono" x="180" y="54" textAnchor="middle">
        babel explore --preparation ID
      </text>
      <path className="rt-flow" d="M180 66V90" markerEnd="url(#rt-arrow-run)" />
      <text className="rt-label" x="188" y="82">starts one run</text>
      <rect className="rt-boundary" x="14" y="96" width="332" height="150" rx="10" />
      <text className="rt-boundary-label" x="30" y="117">
        AI BOUNDARY — THE ONLY PLACE A MODEL RUNS
      </text>
      <rect className="rt-box rt-box--worker" x="30" y="130" width="300" height="102" rx="9" />
      <text className="rt-title" x="180" y="154" textAnchor="middle">One sandboxed Code worker</text>
      <text className="rt-sub" x="180" y="174" textAnchor="middle">
        no network · no host files · no credentials
      </text>
      <text className="rt-sub" x="180" y="196" textAnchor="middle">
        profile — which model, which provider —
      </text>
      <text className="rt-sub" x="180" y="210" textAnchor="middle">
        fixed in your terminal ceremony, never here
      </text>
      <path className="rt-flow rt-flow--exchange" d="M120 248V286" markerEnd="url(#rt-arrow-run-x)" />
      <path className="rt-flow rt-flow--exchange" d="M240 286V248" markerEnd="url(#rt-arrow-run-x)" />
      <text className="rt-exchange-label" x="130" y="271">asks for evidence</text>
      <text className="rt-exchange-label" x="248" y="265">granted or denied,</text>
      <text className="rt-exchange-label" x="248" y="277">receipted</text>
      <rect className="rt-box" x="14" y="290" width="332" height="64" rx="9" />
      <text className="rt-title" x="180" y="312" textAnchor="middle">Babel's evidence broker</text>
      <text className="rt-sub" x="180" y="330" textAnchor="middle">
        checks each request against the run's grant
      </text>
      <text className="rt-sub" x="180" y="344" textAnchor="middle">
        — grants, denies, and receipts every decision
      </text>
      <path className="rt-flow" d="M180 354V378" markerEnd="url(#rt-arrow-run)" />
      <text className="rt-label" x="188" y="370">one supervised stream</text>
      <rect className="rt-box rt-box--records" x="14" y="384" width="332" height="64" rx="9" />
      <text className="rt-title" x="180" y="406" textAnchor="middle">Babel's own records</text>
      <text className="rt-sub" x="180" y="424" textAnchor="middle">
        hypotheses → frontier · observations · run receipt
      </text>
      <text className="rt-sub" x="180" y="438" textAnchor="middle">
        nothing published, nothing written anywhere else
      </text>
      <rect className="rt-endbar" x="14" y="464" width="332" height="42" rx="9" />
      <text className="rt-sub" x="180" y="482" textAnchor="middle">
        The worker is terminated when the run ends.
      </text>
      <text className="rt-end-strong" x="180" y="497" textAnchor="middle">
        Between runs, no agent exists.
      </text>
    </svg>
  );
}

function HelpPage() {
  return (
    // A section of Settings rather than a page of its own. It is reference
    // text: long by intention, read once, and reached deliberately — which is
    // the one kind of page the three-screen rule does not govern.
    <div className="page-section help-section">

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">The frame</p>
            <h2>Creative, fallible, incomplete</h2>
          </div>
        </div>
        <p>
          Everything Babel produces analytically — hypotheses, observations, findings, proposals — is
          an <strong>interpretation recorded for human review</strong>. It is not an audit, not a
          finding of fact, and not a source of truth. Nothing here has been verified or certified,
          and a candidate that reads convincingly is still a candidate.
        </p>
        <p>
          That is why every claim in this interface renders beside its evidence locator, its
          model-stated confidence, and its counter-evidence or the explicit absence of any. Follow
          the locator before believing a claim; the archive it points at is the only authority.
        </p>
        <ul className="help-list">
          <li>
            <strong>Ordering is not evidence.</strong> Novelty, priority and the question inbox's
            rank order what to look at next. They are estimates, never grounds for a conclusion, and
            they never decide whether a candidate exists.
          </li>
          <li>
            <strong>Archive text is untrusted.</strong> A transcript can contain source code,
            credentials, personal data and adversarial instructions. Quoted material is shown inside
            a visibly quoted frame, escaped, and is never treated as an instruction.
          </li>
          <li>
            <strong>Nothing is deleted.</strong> The frontier and the review log are append-only. A
            rejected candidate stays listed and visibly rejected; a superseded fact keeps its
            revision.
          </li>
          <li>
            <strong>Suggestions, never side effects.</strong> Babel opens no issues, edits no
            repositories, rotates no credentials, and publishes nothing. An accepted plan changes
            Babel's own ledger and nothing outside it.
          </li>
        </ul>
      </article>

      <article className="surface" id="runtime-model">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Runtime model</p>
            <h2>When does Babel run?</h2>
          </div>
        </div>
        <p>
          Babel is not a resident agent: no daemon thinks between your visits, and there is nothing
          here to talk to. It runs as <strong>two loops that never overlap</strong>. The first
          archives this host every hour and involves no model at all. The second is an exploration
          run — the only place a model ever executes — and it exists exactly as long as the run
          you started. <a href="#/settings?section=help" onClick={scrollTo("lifecycle")}>The lifecycle</a> below
          follows the records these loops write.
        </p>
        <div className="runtime-loops">
          <figure className="runtime-loop">
            <figcaption>
              <Badge label="no AI · automatic" tone="green" />
              <strong>The archive loop</strong>
              <span className="runtime-loop-sub">
                Runs every hour, attended by nobody, and only copies and describes files.
              </span>
            </figcaption>
            <ArchiveLoopDiagram />
          </figure>
          <figure className="runtime-loop">
            <figcaption>
              <Badge label="AI · only inside the boundary" tone="violet" />
              <strong>An exploration run</strong>
              <span className="runtime-loop-sub">
                Exists only after you ask for one — <code>babel explore</code> in a terminal, or
                Watch — and ends by terminating its worker.
              </span>
            </figcaption>
            <ExploreRunDiagram />
          </figure>
        </div>
        <p className="panel-caption">
          The dashed violet box is the entire AI surface: one sandboxed worker per run, no network,
          talking only to Babel's broker. Between runs the box is empty — no agent exists.
        </p>

        <h3>Am I talking to an AI?</h3>
        <div className="table-scroll">
          <table className="runtime-ai-table">
            <thead>
              <tr>
                <th>Surface</th>
                <th>A model involved?</th>
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {AI_SURFACES.map(({ surface, mono, answer, reason }) => (
                <tr key={surface}>
                  <td className={mono ? "mono" : undefined}>{surface}</td>
                  <td><Badge label={answer} tone={answer === "never" ? "green" : "violet"} /></td>
                  <td>{reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="panel-caption">
          No row says <strong>yes</strong>: Babel has no conversational surface. Even during a run
          you are not talking to the worker — it talks to Babel's broker, and you read what it
          left behind.
        </p>

        <h3>Who holds authority</h3>
        <p>
          Every run is <strong>operator-started</strong>, and since 2026-09-12 there are two
          doors rather than one: <code>babel explore</code> in a terminal, and{" "}
          <strong>Ask for a run</strong> on Watch, which runs this machine's own binary with the
          flags that command takes. The browser assembles no run of its own — same ceilings, same
          profile, same grants, same receipt, attributed to the session that asked — and a
          deployment with no ceilings set refuses both with one sentence. Stopping is graceful or
          it is nothing, and this server can stop only a run it started itself. The{" "}
          <strong>profile</strong> — which model, which provider — is still chosen only through
          an operator's terminal ceremony (<code>babel analysis profile configure</code> for
          exploration, <code>babel titles configure</code> for title inference); no page and no
          API can pick or change it, and inference refuses until that ceremony has happened. A{" "}
          <strong>broker grant is per-run</strong>: a capability the grant never named is
          denied even where policy would allow it, and the receipt records every decision. And
          nothing Babel runs <strong>publishes or writes outside Babel's own records</strong> — no
          issue opened, no repository edited, nothing published.
        </p>
        <p className="help-planned">
          <strong>Planned, not built.</strong> An autonomous conductor that could start runs on
          its own schedule is designed with per-run attributable authority — every run it started
          would name it and be bounded by the same per-run grant. Today it does not exist: a run
          starts because you asked for it, from a terminal or from Watch.
        </p>
      </article>

      <article className="surface" id="lifecycle">
        <div className="section-heading">
          <div>
            <p className="eyebrow">How work flows</p>
            <h2>The lifecycle</h2>
          </div>
        </div>
        <ol className="help-lifecycle">
          <li>
            <strong>Archive</strong> — <code>babel archive push</code> backs this host's session
            files into the encrypted restic repository. The repository is authoritative for what
            exists; recovery needs only restic and the password.
            <Link className="panel-link" to="/settings?section=archive">Settings › Archive →</Link>
          </li>
          <li>
            <strong>Catalog</strong> — Babel discovers, normalizes and hashes sessions without
            modifying their source, then describes each one: harness, workspace, size, title. A
            background scan does this, so a large corpus fills in progressively.
            <Link className="panel-link" to="/sessions">Sessions →</Link>
          </li>
          <li>
            <strong>Prepare</strong> — <code>babel prepare</code> fixes one exploration's corpus
            scope and builds the retrieval index over it. The preparation is an immutable record, so
            two runs over the same scope reference one description of it.
            <Link className="panel-link" to="/watch">Watch →</Link>
          </li>
          <li>
            <strong>Explore</strong> — <code>babel explore</code> runs one exploration inside a
            contained Code worker, under a capability grant and a recipe. Watch can ask for that
            same run: it starts this machine's own binary with the same flags, so the ceilings,
            the profile and the receipt are the terminal's either way. The run outlives the
            browser session that asked for it, and closing the tab stops nothing.
            <Link className="panel-link" to="/watch">Watch →</Link>
            <a className="panel-link" href="#/settings?section=help" onClick={scrollTo("runtime-model")}>
              When does Babel run? ↑
            </a>
          </li>
          <li>
            <strong>Candidates</strong> — every emergent hypothesis is preserved in a resumable
            frontier, in the model's own wording, with the observations and evidence locators that
            develop it. Nothing in the interface is called a frontier: it is a kind of post in the
            feed.
            <Link className="panel-link" to="/?kind=hypothesis">Home →</Link>
          </li>
          <li>
            <strong>Findings and proposals</strong> — developed candidates are consolidated into
            findings, and a finding or a strong claim can carry proposals: what to do about it,
            with prerequisites, verification criteria, risks and open questions. This is Babel's
            output, and the feed is where all of it is read.
            <Link className="panel-link" to="/">Home →</Link>
          </li>
          <li>
            <strong>Decide</strong> — a human rules. Each decision is an appended, attributed
            event; the record's whole history stays readable. What only you can answer is asked
            under Ask, where an answer is kept verbatim and an interpreted plan applies only on
            your explicit acceptance.
            <Link className="panel-link" to="/">Home →</Link>
            <Link className="panel-link" to="/ask/questions">Asked →</Link>
          </li>
        </ol>
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Vocabulary</p>
            <h2>Key concepts</h2>
          </div>
        </div>
        <dl className="help-terms">
          <div>
            <dt>Session · selector</dt>
            <dd>
              One conversation as a harness wrote it. A selector is{" "}
              <code>HARNESS/SOURCE-ID</code>, or any unambiguous suffix of one, and is how every
              surface addresses a session.
            </dd>
          </div>
          <div>
            <dt>Revision</dt>
            <dd>
              Analytical records are immutable. Revising one appends a descendant that names its
              ancestor, so lineage is a chain of whole records rather than a diff log.
            </dd>
          </div>
          <div>
            <dt>Preparation</dt>
            <dd>
              The fixed corpus scope one or more explorations run over, recorded once and referenced
              by identifier so a receipt cannot carry a second, disagreeing copy of it.
            </dd>
          </div>
          <div>
            <dt>Recipe</dt>
            <dd>
              Versioned, reviewable investigation guidance from the cookbook. A recipe structures
              exploration without constraining what discovery may propose; a draft recipe simply is
              not enabled by default. Observations record the recipe that produced them.
            </dd>
          </div>
          <div>
            <dt>Presence · heartbeat</dt>
            <dd>
              While a conductor cycle or an exploration runs, it announces itself into the shared
              catalog and writes a heartbeat. <Link to="/watch">Watch</Link> lists those
              announcements from every machine, this one included. A heartbeat is evidence and never
              an observation: a run that has gone quiet may be working, blocked, or gone, and no
              host can tell those apart — so the page states the age of the last word rather than
              claiming a run is alive. Presence never steers anything; nothing in Babel starts or
              stops a run on another machine.
            </dd>
          </div>
          <div>
            <dt>Receipt</dt>
            <dd>
              One run's immutable provenance record: identifiers, ordering, commit state, and counts
              of what the run retrieved, deferred, rejected and redacted. It makes a run inspectable,
              not reproducible — the same inputs may produce entirely different ideas.
            </dd>
          </div>
          <div>
            <dt>Provenance · citations</dt>
            <dd>
              An observation without a locator is not evidence. Every claim carries the path, line
              and digest of the archive text it rests on, plus counter-evidence or an explicit
              statement that none was found.
            </dd>
          </div>
          <div>
            <dt>Question · answer · plan</dt>
            <dd>
              When ownership, lifecycle or focus context is missing or stale, Babel asks, and{" "}
              <Link to="/?kind=question">the feed</Link> is where those questions are read, and each is answered on its own page. Answers
              are retained verbatim and attributed; a versioned interpreter turns one into a plan,
              and only an operator's explicit acceptance lets that plan touch the ledger. The
              store underneath is the reality ledger, which is the word the CLI still uses
              (<code>babel reality inbox</code>).
            </dd>
          </div>
        </dl>

        <div className="help-vocab">
          <div>
            <h3>Exploration status</h3>
            <p className="muted">Where a candidate is in §4.2's lifecycle.</p>
            <ul className="help-badge-list">
              {STATUSES.map(([status, meaning]) => (
                <li key={status}>
                  <Badge label={status} tone={statusTone(status)} />
                  <span>{meaning}</span>
                </li>
              ))}
            </ul>
          </div>
          <div>
            <h3>Review status</h3>
            <p className="muted">What a human has decided about a record.</p>
            <ul className="help-badge-list">
              {REVIEW_STATUSES.map(([status, meaning]) => (
                <li key={status}>
                  <Badge label={status} tone={reviewTone(status)} />
                  <span>{meaning}</span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Terminal and browser</p>
            <h2>Commands and where they land</h2>
          </div>
        </div>
        <p className="muted">
          Both surfaces reach one implementation, so a command and its page cannot disagree. Where
          the browser acts — asking for a run, stopping one it started, recording a decision or an
          answer — it runs the command's own path under the command's own refusals, and it holds
          no authority the terminal does not: it cannot choose a model profile, and it cannot
          delete archived data.
        </p>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Command</th>
                <th>What it does</th>
                <th>Where it shows</th>
              </tr>
            </thead>
            <tbody>
              {COMMANDS.map(([command, effect, to, label]) => (
                <tr key={command}>
                  <td className="mono">{command}</td>
                  <td>{effect}</td>
                  <td><Link to={to}>{label}</Link></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">From nothing</p>
            <h2>Quickstart</h2>
          </div>
        </div>
        <ol className="help-lifecycle">
          <li>
            <strong>Connect storage.</strong> <code>babel storage configure</code>, then{" "}
            <code>babel archive init</code> once for the deployment. Without a repository this
            interface still works: the archive surfaces report that it is not configured.
          </li>
          <li>
            <strong>Archive and look around.</strong> <code>babel archive push</code>, then open{" "}
            <Link to="/sessions">Sessions</Link>. The catalog scan starts on its own; titles,
            workspaces and sizes fill in as it describes each session.
          </li>
          <li>
            <strong>Scope an exploration.</strong> <code>babel prepare</code> with the selectors you
            care about, or none for a broad scope. The preparation identifier is what{" "}
            <code>babel explore</code> takes.
          </li>
          <li>
            <strong>Explore, then read.</strong> <code>babel explore --preparation ID</code> in a
            terminal, or <strong>Ask for a run</strong> on <Link to="/watch">Watch</Link>, which
            starts the same thing. Its records appear in <Link to="/">the feed</Link> and on{" "}
            <Link to="/watch">Watch</Link> as soon as it commits them.
          </li>
          <li>
            <strong>Decide.</strong> <Link to="/">The feed</Link> arrives showing what needs you,
            in the order it needs answering; accept, reject, defer, refine or ask from the row.
            Every ruling is attributed and appended, so the trail of what you concluded and when
            stays readable.
          </li>
        </ol>
        <p className="panel-caption">
          Stopping: the <strong>Lock &amp; stop</strong> control in the header revokes this
          session and shuts the listener down. The URL never works again; run{" "}
          <code>babel web</code> for a new one.
        </p>
      </article>
    </div>
  );
}

export default HelpPage;
