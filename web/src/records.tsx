import { useCallback, useEffect, useState } from "react";
import {
  decideDisposition,
  getRecordDispositions,
  getRecordRevisions,
  inviteRecord,
  reviveRecord,
  type ProposedAction,
  type RecordDispositions,
  type RecordInvitation,
  type RecordRef,
  type Revision,
  type RevisionChain,
} from "./api";
import { errorMessage, formatTime } from "./format";
import { Badge, statusTone, TimelineEntry, type Tone } from "./analysis";

// Issue #87's record actions, shared by every record page.
//
// Four product rules live here rather than in each page, so no surface can
// state them differently:
//
//   - A record's content is a chain, not a file (§4.7, #87). The history
//     renders every wording with who wrote it and why, and the superseded ones
//     stay readable: nothing here links "current" to "only".
//   - Authorizing is not doing. Accepting a proposed action records that a
//     person authorized it and performs none of it; the draft an accepted
//     draft-issue carries is text, shown on request, never opened and never
//     filed by Babel.
//   - An invitation carries no instruction. The button has no text field
//     beside it, because what to do with the record is the next run's
//     judgement.
//   - Nothing closes. A candidate at rest is revivable, and reviving states
//     why: a rejected candidate quietly reappearing is indistinguishable from
//     one that was never rejected.
//
// Every mutation sends the chain head the page was rendered against. A head
// that moved since is refused by the server with an explanation, which this
// component shows verbatim and then reloads: the operator was looking at words
// that have been replaced, and the honest answer is to show them the new ones
// rather than to record a decision about the old ones.

// RESTING is §4.2's three resting statuses. #87 makes each of them a place a
// candidate can leave, which is why they are listed here as one set rather than
// tested for one at a time.
const RESTING = ["deferred", "rejected", "promoted"];

// KIND_NOTES says, for each proposed action, what accepting it does inside
// Babel and what it does not do outside it. The second half is the load-bearing
// one: every one of these five names a surface an operator's click feeds, and
// none of them reaches past Babel on its own.
const KIND_NOTES: Record<string, string> = {
  "draft-issue":
    "Authorizing records that you approved this draft. Babel files nothing: publishing is your act, in your own browser, under your own credentials.",
  "propose-reality-fact":
    "Authorizing routes this into the Reality Ledger as a proposal. A fact becomes authoritative only through the ledger's own explicit acceptance.",
  "store-memory":
    "Authorizing records that this is worth keeping. Durable memory is written by the ledger, not by this click.",
  "ask-operator-question":
    "Authorizing puts the question in the review inbox. Nothing is answered here.",
  "develop-further":
    "Authorizing records that this deserves more work. Which run does it, and how, stays the loop's judgement.",
};

function rulingTone(status: string): Tone {
  switch (status) {
    case "accepted":
      return "green";
    case "declined":
      return "amber";
    default:
      return "neutral";
  }
}

export function RecordActions({
  record,
  lifecycle,
}: {
  record: RecordRef;
  // lifecycle is the candidate's §4.2 status, absent for record kinds that have
  // none. A finding has no lifecycle to revive, and offering the transition
  // anyway would be a button that can only fail.
  lifecycle?: string;
}) {
  const [chain, setChain] = useState<RevisionChain | null>(null);
  const [actions, setActions] = useState<RecordDispositions | null>(null);
  const [chainError, setChainError] = useState<string | null>(null);
  const [actionsError, setActionsError] = useState<string | null>(null);

  const { type, id } = record;
  // The two reads settle independently. They come from two components of the
  // durable file, and a deployment can hold one without the other: a session
  // whose disposition ledger would not open still has a revision history, and a
  // block that reported both as unavailable because one was would hide a record
  // Babel can read.
  const load = useCallback(async () => {
    const [revisions, dispositions] = await Promise.allSettled([
      getRecordRevisions(type, id),
      getRecordDispositions(type, id),
    ]);
    if (revisions.status === "fulfilled") {
      setChain(revisions.value);
      setChainError(null);
    } else {
      setChainError(errorMessage(revisions.reason));
    }
    if (dispositions.status === "fulfilled") {
      setActions(dispositions.value);
      setActionsError(null);
    } else {
      setActionsError(errorMessage(dispositions.reason));
    }
  }, [type, id]);

  useEffect(() => {
    setChain(null);
    setActions(null);
    setChainError(null);
    setActionsError(null);
    void load();
  }, [load]);

  // The head every mutation confirms. It comes from the server's own answer
  // rather than from the route parameter: the record being read may be a
  // superseded wording, and the action is still about the chain it belongs to.
  const headID = actions?.head_id ?? chain?.head_id ?? "";
  const superseded = headID !== "" && headID !== id;

  return (
    <>
      <RevisionHistoryCard chain={chain} viewing={id} error={chainError} />
      <DispositionsCard
        actions={actions}
        error={actionsError}
        headID={headID}
        superseded={superseded}
        onChanged={load}
      />
      <ProcessFurtherCard
        record={record}
        invitations={actions?.invitations ?? []}
        headID={headID}
        error={actionsError}
        onChanged={load}
      />
      {lifecycle !== undefined && RESTING.includes(lifecycle) && (
        <ReviveCard record={record} lifecycle={lifecycle} headID={headID} onChanged={load} />
      )}
    </>
  );
}

function RevisionHistoryCard({
  chain,
  viewing,
  error,
}: {
  chain: RevisionChain | null;
  viewing: string;
  error: string | null;
}) {
  return (
    <article className="card revisions-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Append-only</p>
          <h2>Revision history</h2>
        </div>
        {chain && <span className="count-label">{chain.revisions.length}</span>}
      </div>
      <p className="muted">
        Every wording this record has had, oldest first. A revision supersedes its predecessor
        and replaces nothing: the earlier wording stays readable at its own identifier.
      </p>
      {error && <p className="inline-error" role="alert">Revision history could not be read: {error}</p>}
      {!chain && !error && <p className="muted"><span className="spinner" /> Reading the chain…</p>}
      {chain && (
        <ol className="timeline revision-timeline">
          {chain.revisions.map((revision) => (
            <RevisionEntry key={revision.id} revision={revision} viewing={viewing} />
          ))}
        </ol>
      )}
    </article>
  );
}

function RevisionEntry({ revision, viewing }: { revision: Revision; viewing: string }) {
  const author = revision.actor.kind === "operator" ? "operator" : "run";
  return (
    <TimelineEntry
      badge={`${author} · revision ${revision.sequence}`}
      tone={revision.actor.kind === "operator" ? "violet" : "cyan"}
      at={revision.recorded_at}
    >
      <span className="mono secondary">{revision.actor.id || "—"}</span>
      <span className="revision-record">
        <span className="mono">{revision.record.id}</span>
        {revision.record.id === viewing && <span className="revision-mark"> — the wording shown above</span>}
        {revision.head && <span className="revision-mark"> — current</span>}
      </span>
      {revision.reason ? (
        <span className="untrusted-inline">{revision.reason}</span>
      ) : (
        <span className="muted">
          The chain's first wording; it supersedes nothing, so it states no reason.
        </span>
      )}
    </TimelineEntry>
  );
}

function DispositionsCard({
  actions,
  error,
  headID,
  superseded,
  onChanged,
}: {
  actions: RecordDispositions | null;
  error: string | null;
  headID: string;
  superseded: boolean;
  onChanged: () => Promise<void>;
}) {
  return (
    <article className="card dispositions-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Proposed next actions</p>
          <h2>Dispositions</h2>
        </div>
        {actions && <span className="count-label">{actions.dispositions.length}</span>}
      </div>
      <p className="muted">
        What a run suggested could be done with this record. Each is a proposal until you answer
        it, and answering it authorizes the action rather than performing it — Babel publishes,
        files and applies nothing.
      </p>
      {superseded && (
        <p className="context-note" role="note">
          <span className="context-label">Superseded wording</span>
          These actions were proposed about the wording shown above, which a later revision has
          replaced. Answering them still records a decision against the chain, whose current
          wording is <span className="mono">{headID}</span>.
        </p>
      )}
      {error && (
        <p className="inline-error" role="alert">Proposed actions could not be read: {error}</p>
      )}
      {!actions && !error && <p className="muted"><span className="spinner" /> Reading proposed actions…</p>}
      {actions && actions.dispositions.length === 0 && (
        <p className="muted">
          No next actions are proposed for this record. That is an absence, not a verdict: a run
          proposes actions when it has one to propose.
        </p>
      )}
      {actions && actions.dispositions.length > 0 && (
        <ul className="disposition-list">
          {actions.dispositions.map((action) => (
            <DispositionEntry
              key={action.id}
              action={action}
              headID={headID}
              onChanged={onChanged}
            />
          ))}
        </ul>
      )}
    </article>
  );
}

function DispositionEntry({
  action,
  headID,
  onChanged,
}: {
  action: ProposedAction;
  headID: string;
  onChanged: () => Promise<void>;
}) {
  const [busy, setBusy] = useState<"accepted" | "declined" | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<string | null>(null);
  const [note, setNote] = useState("");

  async function answer(ruling: "accepted" | "declined") {
    const prompt =
      ruling === "accepted"
        ? `Authorize "${action.summary}"?\n\nThis records that you authorized the action. It ` +
          "does not perform it: nothing is published, filed, or written outside Babel. The " +
          "record is append-only — reconsidering later appends another entry."
        : `Decline "${action.summary}"?\n\nThe proposal stays readable and can be reconsidered; ` +
          "declining appends an entry rather than deleting anything.";
    if (!window.confirm(prompt)) return;
    setBusy(ruling);
    setFailure(null);
    setOutcome(null);
    try {
      const result = await decideDisposition(action.id, ruling, headID, note.trim());
      setOutcome(
        ruling === "accepted"
          ? `Authorized. The action is now ${result.status}, and Babel published ${result.published}.`
          : `Declined. The action is now ${result.status}, and stays readable.`,
      );
      setNote("");
      await onChanged();
    } catch (reason) {
      setFailure(errorMessage(reason));
      // A refusal may be a chain that moved, so the page is re-read either
      // way: the operator has to be able to see the wording that replaced the
      // one they were shown.
      await onChanged();
    } finally {
      setBusy(null);
    }
  }

  const answered = action.ledger.length > 0;
  return (
    <li className="disposition-entry">
      <div className="disposition-heading">
        <Badge label={action.kind} tone="blue" />
        <Badge label={action.status} tone={rulingTone(action.status)} />
        <span className="mono secondary disposition-id">{action.id}</span>
      </div>
      <p className="untrusted-inline disposition-summary">{action.summary}</p>
      {action.rationale && (
        <p className="untrusted-inline disposition-rationale">{action.rationale}</p>
      )}
      <p className="muted disposition-provenance">
        Proposed by {action.proposed_by.kind}{" "}
        <span className="mono">{action.proposed_by.id}</span>
        {formatTime(action.created_at) && <> · {formatTime(action.created_at)?.absolute}</>}
      </p>
      {KIND_NOTES[action.kind] && <p className="muted disposition-effect">{KIND_NOTES[action.kind]}</p>}

      {action.anchor && (
        <p className="disposition-anchor">
          <span className="context-label">Bound repository</span>
          <span className="mono">{action.anchor.url}</span>
          {action.anchor.branch && <span className="mono"> · {action.anchor.branch}</span>}
          <span className="muted"> — read from a checkout on this machine, never from a model's guess.</span>
        </p>
      )}
      {action.draft && (
        <details className="json-disclosure draft-disclosure">
          <summary>Rendered draft (text; Babel files nothing)</summary>
          <pre>{action.draft}</pre>
        </details>
      )}

      {answered && (
        <ol className="timeline ruling-timeline">
          {action.ledger.map((entry) => (
            <TimelineEntry
              key={entry.id}
              badge={entry.ruling}
              tone={rulingTone(entry.ruling)}
              at={entry.recorded_at}
            >
              <span className="mono secondary">{entry.by}</span>
              {entry.note && <span className="untrusted-inline">{entry.note}</span>}
            </TimelineEntry>
          ))}
        </ol>
      )}

      <label className="decide-field">
        Note <span className="muted">(optional, recorded with your decision)</span>
        <textarea
          value={note}
          onChange={(event) => setNote(event.target.value)}
          rows={2}
          aria-label={`Note for ${action.id}`}
        />
      </label>
      <div className="verify-actions disposition-actions">
        <button
          type="button"
          className="primary-button"
          disabled={busy !== null}
          onClick={() => void answer("accepted")}
          data-disposition-accept={action.id}
        >
          {busy === "accepted" ? "Recording…" : "Authorize"}
        </button>
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => void answer("declined")}
          data-disposition-decline={action.id}
        >
          {busy === "declined" ? "Recording…" : "Decline"}
        </button>
      </div>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
      {outcome && <p className="success-note" role="status">{outcome}</p>}
    </li>
  );
}

function ProcessFurtherCard({
  record,
  invitations,
  headID,
  error,
  onChanged,
}: {
  record: RecordRef;
  invitations: RecordInvitation[];
  headID: string;
  error: string | null;
  onChanged: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<string | null>(null);

  const queued = invitations.filter((invitation) => invitation.open);
  const taken = invitations.filter((invitation) => !invitation.open);

  async function invite() {
    setBusy(true);
    setFailure(null);
    setOutcome(null);
    try {
      const result = await inviteRecord(record, headID);
      setOutcome(`Queued. The next run will see this record; the instruction is ${result.instruction}`);
      await onChanged();
    } catch (reason) {
      setFailure(errorMessage(reason));
      await onChanged();
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="card invite-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">One click, no brief</p>
          <h2>Process further</h2>
        </div>
      </div>
      <p className="muted">
        Say this record deserves another look, without saying what to do about it. Refining it,
        questioning it, amending it, or abandoning it stays the next run's judgement — there is
        deliberately nowhere here to write an instruction.
      </p>
      <button
        type="button"
        className="primary-button invite-button"
        disabled={busy || headID === ""}
        onClick={() => void invite()}
        data-invite={record.id}
      >
        {busy ? "Recording…" : "Process further"}
      </button>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
      {outcome && <p className="success-note" role="status">{outcome}</p>}
      {queued.length > 0 && (
        <p className="invite-queued" data-invite-queued={queued.length}>
          <Badge label={queued.length === 1 ? "queued" : `${queued.length} queued`} tone="amber" />
          <span className="muted">
            {queued.length === 1 ? "An invitation is" : "Invitations are"} waiting for the next run
            to take {queued.length === 1 ? "it" : "them"} into scope.
          </span>
        </p>
      )}
      {taken.length > 0 && (
        <ul className="link-list invite-history">
          {taken.map((invitation) => (
            <li key={invitation.id}>
              <Badge label="taken" tone="green" />
              <span className="muted">
                taken by <span className="mono">{invitation.consumed_by}</span>
                {formatTime(invitation.consumed_at ?? "") && <> · {formatTime(invitation.consumed_at ?? "")?.relative}</>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p className="inline-error" role="alert">
          Invitations could not be read in this session: {error}
        </p>
      )}
      {invitations.length === 0 && !outcome && !error && (
        <p className="muted">No invitation is recorded against this record.</p>
      )}
    </article>
  );
}

function ReviveCard({
  record,
  lifecycle,
  headID,
  onChanged,
}: {
  record: RecordRef;
  lifecycle: string;
  headID: string;
  onChanged: () => Promise<void>;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<string | null>(null);

  async function revive() {
    const stated = reason.trim();
    if (!stated) {
      setFailure("A revive states why the candidate deserves to move again.");
      return;
    }
    setBusy(true);
    setFailure(null);
    setOutcome(null);
    try {
      const result = await reviveRecord(record, stated, headID);
      setOutcome(`Revived. The candidate is ${result.event.status} again, and the reason is recorded.`);
      setReason("");
      await onChanged();
    } catch (reason_) {
      setFailure(errorMessage(reason_));
      await onChanged();
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="card revive-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Nothing closes</p>
          <h2>Revive</h2>
        </div>
        <Badge label={lifecycle} tone={statusTone(lifecycle)} />
      </div>
      <p className="muted">
        {lifecycle} is a resting place, not an ending. Returning this candidate to the frontier
        appends a transition and rewrites none: the status history keeps every state it has held,
        including this one.
      </p>
      <label className="decide-field">
        Why it deserves to move again <span className="muted">(required, recorded with the transition)</span>
        <textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          rows={2}
          required
          aria-label="Revive reason"
          data-revive-reason
        />
      </label>
      <p className="muted revive-rule">
        The reason is required because a candidate that can always come back is only safe if
        coming back leaves an argument behind.
      </p>
      <button
        type="button"
        className="primary-button revive-button"
        disabled={busy || headID === ""}
        onClick={() => void revive()}
        data-revive={record.id}
      >
        {busy ? "Recording…" : "Revive onto the frontier"}
      </button>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
      {outcome && <p className="success-note" role="status">{outcome}</p>}
    </article>
  );
}
