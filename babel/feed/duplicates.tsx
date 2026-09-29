import { useRef, useState, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, Stack } from "@manifold/ui";
import {
  ACTIONS,
  DUPLICATE_CLUSTER_MEMBERS,
  DUPLICATE_JUDGEMENTS,
  DuplicateSweepPlanSchema,
  DuplicateSweepReportSchema,
  JEV_ACTIONS,
  JEV_PLUGIN_ID,
  type DuplicateIntent,
  type DuplicateMember,
  type DuplicatePreview,
  type DuplicateSweepReport,
} from "../contract.ts";
import { ask, NO_SEAT, openRecord, refusal } from "./api.ts";

function Members({
  members,
}: {
  members: readonly (DuplicateMember & { title?: string; claim?: string })[];
}): ReactElement {
  return (
    <ul className="babel-duplicate-members">
      {members.map((member) => (
        <li key={member.recordId}>
          {member.title && <strong>{member.title}</strong>}
          {member.claim && <p>{member.claim}</p>}
          <p>
            <code>{member.recordId}</code> · {member.kind} · revision {member.revision}
          </p>
          <p>
            Fingerprint: <code>{member.fingerprint}</code>
          </p>
          <p>
            Run: <code>{member.runId || "unknown"}</code>
          </p>
          <p>Sources: {member.sourceIds.length === 0 ? "unknown" : member.sourceIds.join(", ")}</p>
          {member.unresolvedSources > 0 && (
            <p className="babel-duplicate-unresolved">
              Unresolved cited sessions: {member.unresolvedSources} (not counted as known sources).
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}

function Intent({ intent }: { intent: DuplicateIntent }): ReactElement {
  return (
    <Stack className="babel-duplicate-intent" gap="var(--babel-space-2)">
      <p>
        Proposed representative: <code>{intent.representative}</code>
      </p>
      <Members members={intent.members} />
      <p className="babel-duplicate-audit">
        Independence audit: {intent.audit.records} records · {intent.audit.distinctRuns} distinct
        runs · {intent.audit.distinctSources} distinct sources · {intent.audit.missingRuns} records
        missing runs · {intent.audit.missingSources} records with missing or unresolved sources.
      </p>
      <p className="babel-note">
        Shared or unknown provenance is not independent support. Linking duplicates adds no new
        evidence and does not merge, rewrite, delete or rule on any record.
      </p>
      <ul className="babel-duplicate-evidence">
        {intent.pairs.map((pair) => (
          <li key={`${pair.a}/${pair.b}`}>
            <code>{pair.a}</code> ↔ <code>{pair.b}</code>: {pair.evidence}
          </li>
        ))}
      </ul>
    </Stack>
  );
}

/** A saved intent is not authority. Only a fresh, explicit preview can expose confirmation. */
export function DuplicateAction({
  host,
  nextActionId,
  intent,
  onApplied,
}: {
  host: HostServices;
  nextActionId: string;
  intent: DuplicateIntent;
  onApplied?: () => void;
}): ReactElement {
  const [preview, setPreview] = useState<DuplicatePreview | null>(null);
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const busy = useRef(false);

  async function inspect(): Promise<void> {
    if (busy.current) return;
    busy.current = true;
    setWorking(true);
    setFailure("");
    setPreview(null);
    try {
      setPreview(await ask(host, ACTIONS.duplicatePreview, { nextActionId }));
    } catch (error) {
      setFailure(refusal(error));
    } finally {
      busy.current = false;
      setWorking(false);
    }
  }

  async function apply(): Promise<void> {
    if (busy.current || preview?.state !== "ready") return;
    busy.current = true;
    setWorking(true);
    setFailure("");
    try {
      const application = await ask(host, ACTIONS.duplicateApply, {
        nextActionId,
        fingerprint: preview.fingerprint,
        confirm: true,
      });
      setPreview({ ...preview, state: "applied", application });
      onApplied?.();
    } catch (error) {
      // A stale/refused attempt cannot leave an old confirmation live. Never retry an apply.
      setPreview(null);
      setFailure(`${refusal(error)} Read a fresh preview before confirming again.`);
    } finally {
      busy.current = false;
      setWorking(false);
    }
  }

  return (
    <Stack
      className="babel-duplicate-action"
      data-state={failure !== "" ? "refused" : (preview?.state ?? "unpreviewed")}
      gap="var(--babel-space-2)"
    >
      <Intent intent={preview?.intent ?? intent} />
      <p className="babel-note">
        Duplicate-link authority is separate from the suggestion destination and its Accept or
        Decline ledger. Only an authenticated operator can preview and confirm these links.
      </p>
      <button
        type="button"
        data-duplicate-preview
        disabled={working}
        onClick={() => void inspect()}
      >
        {preview === null ? "Preview duplicate links" : "Read duplicate preview again"}
      </button>
      {preview !== null && (
        <Stack className="babel-duplicate-effect" gap="var(--babel-space-2)">
          <p role="status">
            Duplicate links: {preview.state}. {preview.reason}
          </p>
          <p>
            Preview fingerprint: <code>{preview.fingerprint}</code>
          </p>
          <ul>
            {preview.links.map((link) => (
              <li key={`${link.fromId}/${link.toId}`} data-exists={link.exists}>
                <code>{link.fromId}</code> → <code>{link.toId}</code> · {link.kind} ·{" "}
                {link.exists ? "already exists; unchanged" : "new link in this preview"}
              </li>
            ))}
          </ul>
          {preview.state === "ready" && (
            <div className="babel-confirm">
              <p>
                Confirm exactly {preview.links.filter((link) => !link.exists).length} new
                corroborates links above. Existing links remain unchanged; no record is removed or
                ruled on. The store rechecks every member before applying atomically.
              </p>
              <Cluster gap="var(--babel-space-2)">
                <button
                  type="button"
                  className="babel-primary"
                  data-duplicate-apply
                  disabled={working}
                  onClick={() => void apply()}
                >
                  Confirm duplicate links
                </button>
                <button type="button" disabled={working} onClick={() => setPreview(null)}>
                  Cancel confirmation
                </button>
              </Cluster>
            </div>
          )}
          {preview.application !== null && (
            <div className="babel-duplicate-application">
              <p>
                Applied by {preview.application.operatorId} at {preview.application.at}. This is the
                durable application; reading it again does not add links.
              </p>
              <ul>
                {preview.application.links.map((link) => (
                  <li key={`${link.fromId}/${link.toId}`}>
                    <code>{link.fromId}</code> → <code>{link.toId}</code> · {link.kind}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </Stack>
      )}
      {failure !== "" && (
        <p className="babel-refusal" role="alert">
          {failure}
        </p>
      )}
    </Stack>
  );
}

/** Free planning, one explicitly bounded paid dispatch, then separate durable submission. */
export function JevDuplicates({ host }: { host: HostServices }): ReactElement | null {
  const [limit, setLimit] = useState(String(DUPLICATE_CLUSTER_MEMBERS));
  const [cut, setCut] = useState("");
  const [judgements, setJudgements] = useState(String(DUPLICATE_JUDGEMENTS));
  const [request, setRequest] = useState({ after: "", limit: DUPLICATE_CLUSTER_MEMBERS, ids: [] });
  const [report, setReport] = useState<DuplicateSweepReport | null>(null);
  const [proposals, setProposals] = useState<DuplicateSweepReport["suggestions"]>([]);
  const [saved, setSaved] = useState<{ id: string; recordId: string; intent: DuplicateIntent }[]>(
    [],
  );
  const [working, setWorking] = useState(false);
  const [message, setMessage] = useState("");
  const [failure, setFailure] = useState("");
  const busy = useRef(false);
  const [planRead, setPlanRead] = useState(0);
  const plan = usePolledResource(
    async () => {
      try {
        const result = await host.client.action(
          `${JEV_PLUGIN_ID}.${JEV_ACTIONS.duplicatesPlan}`,
          request,
        );
        const parsed = result.ok ? DuplicateSweepPlanSchema.safeParse(result.result) : null;
        if (parsed?.success && parsed.data.silent === "") return parsed.data.plan;
      } catch {
        // Missing, disabled or unfunded Jev contributes no empty section to Feed.
      }
      return null;
    },
    15_000,
    {
      key: `babel-duplicate-plan:${JSON.stringify(request)}:${planRead}`,
      initial: null,
      hold: () => busy.current || report !== null,
    },
  );
  const confidence = Number(cut);
  const budget = Number(judgements);
  const size = Number(limit);
  const validLimit = Number.isInteger(size) && size >= 2 && size <= DUPLICATE_CLUSTER_MEMBERS;
  const validBound =
    cut.trim() !== "" &&
    confidence >= 0 &&
    confidence <= 1 &&
    Number.isInteger(budget) &&
    budget >= 1 &&
    budget <= DUPLICATE_JUDGEMENTS;

  function readPlan(after: string): void {
    if (busy.current || !validLimit || proposals.length > 0) return;
    setReport(null);
    setFailure("");
    setMessage("");
    setRequest({ after, limit: size, ids: [] });
    setPlanRead((current) => current + 1);
  }

  async function judge(): Promise<void> {
    if (
      busy.current ||
      report !== null ||
      !validBound ||
      plan.value === null ||
      plan.value.candidates.length < 2
    ) {
      return;
    }
    busy.current = true;
    setWorking(true);
    setFailure("");
    setMessage("");
    try {
      const members = plan.value.candidates.map(
        ({ title: _title, claim: _claim, ...member }) => member,
      );
      const result = await host.client.action(`${JEV_PLUGIN_ID}.${JEV_ACTIONS.duplicates}`, {
        members,
        cut: confidence,
        judgements: budget,
      });
      if (!result.ok) throw new Error(result.denial.message);
      const answer = DuplicateSweepReportSchema.parse(result.result);
      setReport(answer);
      setProposals(answer.suggestions);
    } catch (error) {
      setFailure(refusal(error));
    } finally {
      busy.current = false;
      setWorking(false);
    }
  }

  async function submit(): Promise<void> {
    if (busy.current || proposals.length === 0) return;
    busy.current = true;
    setWorking(true);
    setFailure("");
    let delivered = 0;
    try {
      for (const proposal of proposals) {
        const result = await ask(host, ACTIONS.suggest, proposal);
        delivered += 1;
        if (proposal.intent !== undefined) {
          const entry = { id: result.id, recordId: result.recordId, intent: proposal.intent };
          setSaved((current) => [...current.filter((item) => item.id !== entry.id), entry]);
        }
      }
      setMessage(`Submitted ${delivered} duplicate suggestions. No links have been applied.`);
    } catch (error) {
      setFailure(`${delivered} submitted. ${refusal(error)}`);
    } finally {
      setProposals((current) => current.slice(delivered));
      busy.current = false;
      setWorking(false);
    }
  }

  if (plan.value === null && report === null && saved.length === 0 && failure === "") return null;
  return (
    <Stack className="babel-jev-duplicates babel-jev-sweep" gap="var(--babel-space-2)">
      <h2>Duplicate records</h2>
      <p className="babel-note">
        Planning is free. Judge runs one bounded pass over the displayed members, never an automatic
        paid loop. Judgement and suggestion submission change no graph links.
      </p>
      {plan.value !== null && (
        <>
          <p>
            {plan.value.eligible} eligible records; {plan.value.candidates.length} in this page. At
            most {plan.value.maxPairs} candidate pairs and up to{" "}
            {plan.value.newSuggestionsUpperBound} new suggestions before judgement.
          </p>
          <Members members={plan.value.candidates} />
          <Cluster gap="var(--babel-space-2)">
            <label>
              Members per free plan
              <input
                type="number"
                min={2}
                max={DUPLICATE_CLUSTER_MEMBERS}
                value={limit}
                disabled={working}
                onInput={(event) => setLimit(event.currentTarget.value)}
              />
            </label>
            <button
              type="button"
              data-duplicate-plan
              disabled={working || !validLimit || proposals.length > 0}
              onClick={() => readPlan("")}
            >
              Read free duplicate plan
            </button>
            {plan.value.continuation !== "" && (
              <button
                type="button"
                data-duplicate-next
                disabled={working || !validLimit || proposals.length > 0}
                onClick={() => readPlan(plan.value?.continuation ?? "")}
              >
                Read next free page
              </button>
            )}
          </Cluster>
          <Cluster gap="var(--babel-space-2)">
            <label>
              Confidence cut (required, 0–1)
              <input
                type="number"
                min={0}
                max={1}
                step="any"
                value={cut}
                disabled={working}
                onInput={(event) => setCut(event.currentTarget.value)}
              />
            </label>
            <label>
              Judgement budget
              <input
                type="number"
                min={1}
                max={DUPLICATE_JUDGEMENTS}
                value={judgements}
                disabled={working}
                onInput={(event) => setJudgements(event.currentTarget.value)}
              />
            </label>
            <button
              type="button"
              data-duplicate-judge
              disabled={
                working || !validBound || report !== null || plan.value.candidates.length < 2
              }
              onClick={() => void judge()}
            >
              Judge this duplicate page
            </button>
          </Cluster>
          {validBound && (
            <p className="babel-note">
              This pass permits at most {Math.min(budget, plan.value.maxPairs)} paid pair judgements
              at confidence cut {confidence}. A cut is not a probability that a claim is true.
            </p>
          )}
        </>
      )}
      {report !== null && (
        <p role="status">
          {report.candidates} candidate pairs; {report.attempted} attempted; {report.judged} judged.
          {report.truncated
            ? " Budget truncated this pass."
            : report.stopped === ""
              ? " This bounded pass is complete."
              : ""}
          {report.stopped !== "" && ` Stopped: ${report.stopped}`}
          {` Exactly ${report.suggestions.length} suggestions returned; ${proposals.length} await submission.`}
        </p>
      )}
      {proposals.map((proposal) => (
        <section className="babel-duplicate-draft" key={proposal.recordId}>
          <h3>{proposal.summary}</h3>
          <p>{proposal.rationale}</p>
          <p className="babel-note">
            Unsubmitted duplicate-link intent; suggested next action: {proposal.kind}.
            Projection/export and apply authority are separate.
          </p>
          {proposal.intent !== undefined && (
            <>
              <Intent intent={proposal.intent} />
              <p>Proposed links (existence is checked by the saved suggestion’s preview):</p>
              <ul>
                {proposal.intent.members
                  .filter((member) => member.recordId !== proposal.intent?.representative)
                  .map((member) => (
                    <li key={member.recordId}>
                      <code>{proposal.intent?.representative}</code> →{" "}
                      <code>{member.recordId}</code> · corroborates
                    </li>
                  ))}
              </ul>
            </>
          )}
        </section>
      ))}
      {proposals.length > 0 && (
        <Cluster gap="var(--babel-space-2)">
          <button
            type="button"
            data-duplicate-submit
            disabled={working}
            onClick={() => void submit()}
          >
            Submit {proposals.length} duplicate suggestions
          </button>
          <button type="button" disabled={working} onClick={() => setProposals([])}>
            Discard duplicate preview
          </button>
        </Cluster>
      )}
      {saved.map((entry) => (
        <section key={entry.id} className="babel-duplicate-saved">
          <p>
            Saved suggestion <code>{entry.id}</code>
          </p>
          <button
            type="button"
            onClick={() => {
              if (openRecord(host, entry.recordId) === "no_tile") setFailure(NO_SEAT);
            }}
          >
            Open saved duplicate suggestion
          </button>
          <DuplicateAction host={host} nextActionId={entry.id} intent={entry.intent} />
        </section>
      ))}
      {message !== "" && <p role="status">{message}</p>}
      {failure !== "" && (
        <p className="babel-refusal" role="alert">
          {failure}
        </p>
      )}
    </Stack>
  );
}
