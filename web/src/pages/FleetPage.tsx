import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getPresence, type PresenceFreshness, type PresenceResponse, type PresenceRow } from "../api";
import { errorMessage, formatTime } from "../format";
import { AuthorityMark, Badge, type Tone, useFleetHosts } from "../analysis";

// The Fleet view: what every machine in the deployment says it is running right
// now (issue #118).
//
// # Why this is a page rather than a panel or a card
//
// Every other surface here reads this machine's durable state. The Dashboard
// says so in its own eyebrow — "This machine" — and is bound by its own
// contract to one /api/overview snapshot of state six local services already
// held; presence is neither local nor durable nor in that document, so a panel
// there would have to break the page's rule and carry a second, separately
// degrading fetch the panel grid has no vocabulary for.
//
// Explore was the other candidate, because #109 put the fleet *records* card
// there rather than on a page of its own: that card answers the question the
// receipt strip beside it answers, one scope wider, and splitting them would
// have split one question in two. Presence is a different question — not "what
// has this deployment produced" but "what is happening, and can this host even
// tell" — so a page of its own splits nothing. Two things settle it: Explore is
// gated on this machine having durable analysis storage configured, and presence
// must answer on a machine that has none; and half the rows here are conductor
// cycles, which are not Explore's subject at all.
//
// # What a row is allowed to claim
//
// Nothing on this page asserts that anything is running. A row is a claim a
// process made at a moment, and the only honest rendering states the moment: the
// age comes from the server, the classification comes from internal/presence,
// and past the staleness threshold the row says in words that it cannot tell.
// There is deliberately no green/red liveness dot — a colour would be an
// assertion about a process on another machine that nothing here observed.
//
// The one exception is where the colour is about the *evidence* rather than the
// process, and it is labelled as such: a freshness badge grades how old the
// last word is, which is a fact this page read.

// FRESHNESS_TONES colours the four classifications, and what they grade is the
// age of the evidence — never the health of a process.
//
// Fresh is green because a heartbeat seconds old is strong evidence. Stale is
// amber because the evidence has weakened and that is worth noticing. Lost is
// violet rather than red for the reason internal/presence does not call it
// "dead": red is the vocabulary this interface uses for a rejected record, a
// state somebody decided, and nobody decided this — it is the absence of news.
// Finished is neutral because a row that reported how it ended is not in a
// lesser state than one still going.
const FRESHNESS_TONES: Record<PresenceFreshness, Tone> = {
  fresh: "green",
  stale: "amber",
  lost: "violet",
  finished: "neutral",
};

// FRESHNESS_TITLES say what each classification does and does not establish,
// because the words alone do not. An operator reading "lost" has to be able to
// learn that nothing observed a death.
const FRESHNESS_TITLES: Record<PresenceFreshness, string> = {
  fresh: "This run reported itself recently. That is strong evidence it is alive and still not an observation of it.",
  stale: "Nothing has been heard from this run for a while. It may be working, blocked, or gone; this host cannot tell.",
  lost: "Nothing has been heard from this run for long enough that it has probably gone without finalizing. Nothing observed that, and no process here erased the row.",
  finished: "This run reported how it ended. Its age says how long ago that was and nothing about liveness.",
};

const KIND_TONES: Record<string, Tone> = {
  conductor: "cyan",
  explore: "blue",
};

// STATE_TONES colours what the run last said about itself. Cancelled is amber
// and failed is red because they are different endings: a cancelled run kept
// everything it had already committed, and rendering it as a failure would
// misreport the most common way a long run ends.
const STATE_TONES: Record<string, Tone> = {
  running: "cyan",
  finished: "green",
  failed: "red",
  cancelled: "amber",
};

// formatAge renders a server-computed age at the precision the judgement is
// made at and no finer, mirroring the CLI's own column exactly so the two
// surfaces read the same.
//
// It takes the seconds the server sent and never a timestamp, which is the
// whole point: internal/presence computes the age inside its query against the
// catalog's own clock, so a browser on a machine whose clock is wrong cannot
// turn a live run into a lost one. formatTime is used for the absolute
// timestamps beside it, where browser-relative wording is what every other page
// already shows.
function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 60) return "<1m";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return minutes % 60 === 0 ? `${hours}h` : `${hours}h${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return hours % 24 === 0 ? `${days}d` : `${days}d${hours % 24}h`;
}

// LastSeen is the cell that must not lie, and it is one component so that the
// rule lives in one place.
//
// A running row past the staleness threshold carries the sentence, not a colour:
// "4m ago — running or dead, this host cannot tell" is a claim about evidence,
// where a red dot would be a claim about a process nobody observed. A fresh row
// carries no disclaimer, because there is nothing to disclaim about a heartbeat
// seconds old, and a finished row carries none either, because its age is simply
// when it ended.
//
// The threshold is not applied here. The classification arrived from the server,
// and this only chooses the sentence that matches the word — which is what keeps
// the page from becoming a second classifier.
function LastSeen({ row }: { row: PresenceRow }) {
  const age = formatAge(row.heartbeat_age_seconds);
  const doubtful = row.freshness === "stale" || row.freshness === "lost";
  return (
    <span className="presence-seen">
      <span className="presence-age">{age} ago</span>
      {doubtful && (
        <span className="presence-doubt">running or dead, this host cannot tell</span>
      )}
    </span>
  );
}

// HostGroup is one machine and the runs it announced.
//
// Grouping is by host rather than by run for the operator's own question: "is
// anything running on the laptop" is answered by finding one heading, where a
// flat list sorted by heartbeat interleaves three machines and makes the same
// question a scan. The display name comes from the host vocabulary the fleet
// routes already serve; presence stores no second copy of host identity, so a
// machine that has registered no display name shows its id and nothing invented.
function HostGroup({
  host,
  display,
  local,
  rows,
}: {
  host: string;
  display: string | undefined;
  local: boolean;
  rows: PresenceRow[];
}) {
  // The count is of claims, not of live processes, and the caption below the
  // table says so once for the whole page rather than per row.
  const running = rows.filter((row) => row.state === "running").length;
  return (
    <article className={local ? "card presence-host-card local-host-card" : "card presence-host-card"}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">Host</p>
          <h2>
            <span className="untrusted-inline">{display || host}</span>
            {local && <span className="secondary presence-this-host">this host</span>}
          </h2>
          {display && display !== host && (
            <p className="panel-caption mono-caption untrusted-inline">{host}</p>
          )}
        </div>
        <span className="count-label">
          {running} {running === 1 ? "run announced" : "runs announced"}
          {rows.length > running && (
            <span className="secondary">{rows.length - running} ended</span>
          )}
        </span>
      </div>
      <div className="table-scroll">
        <table className="presence-table">
          <thead>
            <tr>
              <th>Kind</th>
              <th>Run</th>
              <th>Recipe</th>
              <th>Authority</th>
              <th>State</th>
              <th>Last seen</th>
              <th>Receipt</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              // The key is the presence id and never the run id. One conductor
              // cycle announces twice — the loop's own row and the run inside
              // it, under one run id — and they are two facts, because the loop
              // can be alive while the run it started is not.
              <tr key={row.id} className={`presence-row freshness-${row.freshness}`}>
                <td><Badge label={row.kind} tone={KIND_TONES[row.kind] ?? "neutral"} /></td>
                <td className="mono untrusted-inline">{row.run_id}</td>
                <td className="presence-recipe">
                  {row.recipe
                    ? <span className="untrusted-inline">{row.recipe}</span>
                    : <span className="not-observed">no recipe announced</span>}
                  {row.preparation_id && (
                    <span className="secondary mono untrusted-inline">{row.preparation_id}</span>
                  )}
                </td>
                <td><AuthorityMark authority={row.authority} /></td>
                <td>
                  <Badge label={row.state} tone={STATE_TONES[row.state] ?? "neutral"} />
                  <span
                    className="presence-freshness"
                    title={FRESHNESS_TITLES[row.freshness]}
                  >
                    <Badge label={row.freshness} tone={FRESHNESS_TONES[row.freshness] ?? "neutral"} />
                  </span>
                </td>
                <td><LastSeen row={row} /></td>
                <td>
                  {/* A finalized row names the record it committed. It is
                      rendered as an identifier rather than a link because this
                      build has no page for a receipt record: web/src/
                      references.tsx's rule is that a destination is built from
                      a namespace this app actually renders, and inventing one
                      would link into the catch-all redirect. The Explore
                      receipt strip is where a receipt is read here, so that is
                      where the row points. */}
                  {row.receipt_record_id ? (
                    <span className="presence-receipt">
                      <span className="mono untrusted-inline">{row.receipt_record_id}</span>
                      <Link className="panel-link" to="/explore">Receipts →</Link>
                    </span>
                  ) : (
                    <span className="not-observed">not committed yet</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </article>
  );
}

// FleetEmpty explains what presence is, which is the one empty state on this
// interface that has to teach rather than merely report.
//
// An operator reaching an empty Fleet page has two candidate explanations and
// only one of them is right: the deployment is idle, or presence is not working
// here. So the copy names the window the read covers, and says that this
// machine's own runs announce here too — which is what makes "nothing here"
// mean "nothing is running" rather than "this page does not see me".
function FleetEmpty({ retentionSeconds }: { retentionSeconds: number }) {
  return (
    <div className="state-card empty-state presence-empty">
      <span className="empty-icon" aria-hidden="true">◌</span>
      <strong>No machine is announcing a run</strong>
      <span>
        A conductor cycle and an exploration announce themselves into the shared catalog while they
        work, heartbeat while they run, and link their receipt when they finish. This list covers
        the last {formatAge(retentionSeconds)}.
      </span>
      <span>
        This machine's own runs appear here too, so an empty list means the deployment is idle —
        not that this page cannot see you.
      </span>
      <span className="secondary">
        Start one from a terminal with <code>babel explore</code> or <code>babel conductor run</code>.
      </span>
    </div>
  );
}

// PresenceNotice is what the page says when the rows are not what the fleet
// announced. The server's own sentence is rendered rather than a generic
// failure line, because the three cases call for three different actions and
// only the server knows which happened.
//
// It is a notice and not an error state. Local mode is this deployment's shape,
// and a catalog that could not be read is the answer "this host cannot tell" —
// neither is a page that failed to load, and styling them as one would train the
// operator to distrust a working machine.
function PresenceNotice({ data }: { data: PresenceResponse }) {
  return (
    <div className="state-card scope-notice presence-notice" role="status">
      <strong>
        {data.configured
          ? "This host cannot see what the fleet is running"
          : "This machine has no shared backend configured"}
      </strong>
      <span>{data.unavailable}</span>
      {data.configured && (
        <span className="secondary">
          Runs on other machines are unaffected by this, and their receipts still commit — so
          records may appear in Hypotheses and Review while this page stays blank.
        </span>
      )}
    </div>
  );
}

function FleetPage() {
  const [data, setData] = useState<PresenceResponse | null>(null);
  const [readAt, setReadAt] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const fleet = useFleetHosts();

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getPresence()
      .then((value) => {
        setData(value);
        setReadAt(new Date().toISOString());
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  // The page does not poll, and says when it last read instead.
  //
  // That is a deliberate trade rather than an omission. Every age on screen was
  // computed by the catalog at read time, so a page that refreshed itself would
  // be the only thing keeping them current — and one open browser per machine
  // polling a PostgreSQL the whole fleet shares is real cost for a surface
  // nobody watches continuously. What must not happen is an operator reading a
  // frozen age as a current one, so the read time is stated beside the refresh
  // control rather than left implicit.
  const read = formatTime(readAt);

  // The host vocabulary supplies display names. A machine present in the
  // presence table and absent from the vocabulary is normal — it has announced
  // a run and committed no record yet — and it renders its id.
  const names = new Map(fleet.hosts.filter((host) => host.attributed).map((host) => [host.host_id, host.host]));

  // Grouped in first-seen order, which is the reader's order: internal/presence
  // returns running rows before finished ones, newest heartbeat first, so the
  // busiest machine leads and this machine's own row is wherever its heartbeat
  // puts it rather than pinned to the top. Pinning would answer a question the
  // page is not asking.
  const groups: Array<{ host: string; local: boolean; rows: PresenceRow[] }> = [];
  for (const row of data?.rows ?? []) {
    const group = groups.find((candidate) => candidate.host === row.host);
    if (group) {
      group.rows.push(row);
      continue;
    }
    groups.push({ host: row.host, local: row.local_host, rows: [row] });
  }

  return (
    <section className="page fleet-presence-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Every machine</p>
          <h1>Fleet</h1>
          <p className="subtitle">
            What each host says it is running, read from the shared catalog. Nothing here starts,
            stops or steers a run on another machine — this is state to read, and a row is a claim
            a process made at a moment rather than something this host observed.
          </p>
        </div>
        <div className="heading-meta">
          {data?.available && (
            <span className="count-label">
              {data.running} {data.running === 1 ? "run announced" : "runs announced"}
            </span>
          )}
          {read && (
            <span className="refresh-time" title={read.absolute}>
              read {read.relative}
            </span>
          )}
          <button type="button" onClick={load} disabled={loading}>
            {loading && <span className="spinner small" />}
            {loading ? "Reading…" : "Refresh"}
          </button>
        </div>
      </div>

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the fleet…</div>
      )}
      {error && !data && (
        <div className="state-card error-state">
          <strong>The fleet could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}

      {data && !data.available && <PresenceNotice data={data} />}

      {data?.available && (
        <>
          {/* The legend states the thresholds the server classified by, in the
              server's own numbers. A page carrying its own copy of "two
              minutes" would eventually contradict the badge beside it. */}
          <article className="card presence-legend">
            <div className="section-heading">
              <div>
                <p className="eyebrow">How to read this</p>
                <h2>A heartbeat is evidence, not a pulse</h2>
              </div>
            </div>
            <p className="muted">
              A run writes a heartbeat while it works. A missing heartbeat means the run is slow,
              the network is down, the machine slept, or the process died — and no host can tell
              those apart, so this page never says which. What it grades is how old the last word
              is.
            </p>
            <ul className="presence-legend-list">
              <li>
                <Badge label="fresh" tone={FRESHNESS_TONES.fresh} />
                <span>heard from within {formatAge(data.stale_after_seconds)}</span>
              </li>
              <li>
                <Badge label="stale" tone={FRESHNESS_TONES.stale} />
                <span>
                  quiet for more than {formatAge(data.stale_after_seconds)} — working, blocked or
                  gone, and this host cannot tell
                </span>
              </li>
              <li>
                <Badge label="lost" tone={FRESHNESS_TONES.lost} />
                <span>
                  quiet for more than {formatAge(data.lost_after_seconds)} — probably gone without
                  finalizing. Nothing observed that, and nothing here erases the row
                </span>
              </li>
              <li>
                <Badge label="finished" tone={FRESHNESS_TONES.finished} />
                <span>reported how it ended; its age says when, not whether it is alive</span>
              </li>
            </ul>
            <p className="panel-caption">
              A conductor cycle and the run inside it announce separately and share a run id, so one
              cycle is two rows: the loop can be alive while the run it started is not. A parked or
              idle cycle announces nothing at all, so a host with no conductor row is not a host
              whose loop is down.
            </p>
          </article>

          {groups.length === 0
            ? <FleetEmpty retentionSeconds={data.retention_seconds} />
            : groups.map((group) => (
                <HostGroup
                  key={group.host}
                  host={group.host}
                  display={names.get(group.host)}
                  local={group.local}
                  rows={group.rows}
                />
              ))}
        </>
      )}
    </section>
  );
}

export default FleetPage;
