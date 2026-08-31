import { Link } from "react-router-dom";
import { Badge, statusTone, reviewTone } from "../analysis";

// The concepts page. It is documentation rather than a view: nothing here reads
// the API, so it renders identically on a machine with no archive, no analysis
// state, and no network. That is deliberate — the page an operator reaches when
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
  ["babel web", "Serves this interface on loopback with a one-time token.", "/", "Dashboard"],
  ["babel storage configure", "Stores the repository and catalog configuration.", "/archive", "Archive"],
  ["babel archive push", "Backs this host's sessions into the restic repository.", "/archive", "Archive"],
  ["babel archive status", "Reports snapshots per host and the catalog's lag.", "/archive", "Archive"],
  ["babel archive verify", "Checks repository integrity, standard or deep.", "/archive", "Archive"],
  ["babel sessions list", "Lists what this machine's harnesses have written.", "/sessions", "Sessions"],
  ["babel sessions inspect", "Shows one session whole, with its transcript.", "/sessions", "Sessions"],
  ["babel sessions fetch", "Restores one archived session's files locally.", "/sessions", "Sessions"],
  ["babel prepare", "Fixes an exploration's corpus scope and builds its index.", "/explore", "Explore"],
  ["babel explore", "Runs one exploration through the Code worker.", "/explore", "Explore"],
  ["babel hypotheses", "Lists the candidate frontier.", "/hypotheses", "Hypotheses"],
  ["babel findings", "Lists consolidated findings.", "/findings", "Hypotheses → Findings"],
  ["babel review queue", "Lists records awaiting a human decision.", "/review", "Review"],
  ["babel review decide", "Appends one attributed decision.", "/review", "Review"],
  ["babel reality inbox", "Lists the prioritized question inbox.", "/reality", "Reality"],
  ["babel reality answer", "Records an attributed answer, verbatim.", "/reality", "Reality"],
  ["babel export", "Renders one record as JSON or Markdown.", "/review", "Review"],
];

function HelpPage() {
  return (
    <section className="page help-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Orientation</p>
          <h1>What Babel is — and what it is not</h1>
          <p className="subtitle">
            Babel is an exploratory instrument for archived agent conversations. It records where an
            idea came from and how it was investigated; it does not promise the idea is correct.
          </p>
        </div>
      </div>

      <article className="card help-card">
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

      <article className="card help-card">
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
            <Link className="panel-link" to="/archive">Archive →</Link>
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
            <Link className="panel-link" to="/explore">Explore →</Link>
          </li>
          <li>
            <strong>Explore</strong> — <code>babel explore</code> runs one exploration inside a
            contained Code worker, under a capability grant and a recipe. Exploration cannot be
            started from this interface: a run outlives the browser session, and the disclosure
            consent belongs to a terminal.
            <Link className="panel-link" to="/explore">Explore →</Link>
          </li>
          <li>
            <strong>Hypotheses</strong> — every emergent candidate is preserved in a resumable
            frontier, in the model's own wording, with the observations and evidence locators that
            develop it. Consolidated candidates become findings and proposals.
            <Link className="panel-link" to="/hypotheses">Hypotheses →</Link>
          </li>
          <li>
            <strong>Review</strong> — a human decides. Each decision is an appended, attributed
            event; the record's whole history stays readable. Reality questions collect missing
            context, and an interpreted plan applies only on explicit acceptance.
            <Link className="panel-link" to="/review">Review →</Link>
          </li>
        </ol>
      </article>

      <article className="card help-card">
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
            <dt>Reality question · plan</dt>
            <dd>
              When ownership, lifecycle or focus context is missing or stale, Babel asks. Answers are
              retained verbatim and attributed; a versioned interpreter turns one into a plan, and
              only an operator's explicit acceptance lets that plan touch the ledger.
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

      <article className="card help-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Terminal and browser</p>
            <h2>Commands and where they land</h2>
          </div>
        </div>
        <p className="muted">
          Both surfaces reach one implementation, so a command and its page cannot disagree. The
          browser holds no authority the terminal does not: it never starts an exploration, and it
          cannot delete archived data.
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

      <article className="card help-card">
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
            terminal. Its records appear in <Link to="/hypotheses">Hypotheses</Link> and{" "}
            <Link to="/explore">Explore</Link> as soon as it commits them.
          </li>
          <li>
            <strong>Decide.</strong> Work <Link to="/review">Review</Link> and{" "}
            <Link to="/reality">Reality</Link>. Every decision is attributed and appended, so the
            trail of what you concluded and when stays readable.
          </li>
        </ol>
        <p className="panel-caption">
          Stopping: the <strong>Lock &amp; stop</strong> control in the header revokes the launch
          token and shuts the listener down. The URL never works again; run <code>babel web</code>{" "}
          for a new one.
        </p>
      </article>
    </section>
  );
}

export default HelpPage;
