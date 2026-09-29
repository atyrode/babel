import type { HostServices } from "@manifold/plugin";
import { Cluster, Stack } from "@manifold/ui";
import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  ACTIONS,
  ALLOCATION_EXCLUSIONS,
  AllocationPreviewInputSchema,
  AllocationPreviewSchema,
  AllocationVersionInputSchema,
  AllocationVersionResultSchema,
  AllocationVersionSchema,
  SaveAllocationInputSchema,
  door,
  type Activity,
  type AllocationPreview,
  type AllocationPreviewInput,
  type AllocationVersion,
} from "../contract.ts";
import { act } from "./api.ts";

const percent = (fraction: number) => `${(fraction * 100).toFixed(2)}%`;
const displacement = (fraction: number) =>
  `${fraction > 0 ? "+" : ""}${(fraction * 100).toFixed(2)} pp`;

type Slice = AllocationPreview["slices"][number];

const FEEDBACK_LABEL: Record<(typeof ALLOCATION_EXCLUSIONS)[number], string> = {
  counted: "Eligible outcomes",
  administrative: "Excluded: administrative approvals",
  "not-decided": "Excluded: not accepted or rejected",
  "no-operator": "Excluded: unknown operator",
  "no-producing-activity": "Excluded: unknown activity attribution",
  "outside-window": "Excluded: older than seven days",
};

/** Presentation of the door's answer, not a second eligibility or allocation algorithm. */
function Plan({
  plan,
  editor,
}: {
  readonly plan: AllocationPreview;
  readonly editor?: (slice: Slice) => ReactNode;
}) {
  const { coverage } = plan.snapshot;
  const exclusions = new Map(coverage.exclusions.map(({ reason, roots }) => [reason, roots]));
  return (
    <Stack gap="var(--babel-space-3)">
      <p className="plugin-atyrode_babel_watch__muted" data-field="allocation-window">
        Rolling seven-day basis: <time dateTime={plan.snapshot.cutoff}>{plan.snapshot.cutoff}</time>
        {" through "}
        <time dateTime={plan.snapshot.at}>{plan.snapshot.at}</time> (inclusive). This is a feedback
        age limit, not a waiting period or budget duration.
      </p>
      <p>
        Policy {plan.snapshot.policyVersion || "default"} · revision {plan.snapshot.policySeq} ·
        algorithm <code>{plan.algorithm}</code>. Discretionary remainder{" "}
        <strong>{percent(plan.discretionaryFraction)}</strong>; unallocated{" "}
        <strong data-field="allocation-unallocated">{percent(plan.unallocatedFraction)}</strong>.
      </p>
      <p className="plugin-atyrode_babel_watch__muted">
        Minimum {plan.minimumSample} attributable decisions per activity, and two sufficiently
        sampled adjustable activities. Sparse or missing feedback retains the baseline before owner
        edits. Maximum damping {percent(plan.maximumDamping)}, prior sample {plan.priorSample}.
        Acceptance is an operator choice, not verified implementation.
      </p>
      <div className="plugin-atyrode_babel_watch__allocation-slices">
        {plan.slices.map((slice) => (
          <Stack
            key={slice.activity}
            gap="var(--babel-space-2)"
            className="plugin-atyrode_babel_watch__allocation-slice"
            data-activity={slice.activity}
          >
            <h3>
              {slice.activity}
              {slice.enabled ? "" : " · disabled"}
            </h3>
            <dl className="plugin-atyrode_babel_watch__allocation-figures">
              <div>
                <dt>Baseline</dt>
                <dd>{percent(slice.baseline)}</dd>
              </div>
              <div>
                <dt>Protected floor</dt>
                <dd>{percent(slice.protected)}</dd>
              </div>
              <div>
                <dt>Baseline discretionary</dt>
                <dd>{percent(slice.baseline - slice.protected)}</dd>
              </div>
              <div>
                <dt>Proposed spend</dt>
                <dd data-field="proposed">{percent(slice.proposed)}</dd>
              </div>
              <div>
                <dt>Proposed discretionary</dt>
                <dd>{percent(slice.proposed - slice.protected)}</dd>
              </div>
              <div>
                <dt>Displaced from baseline</dt>
                <dd data-field="displacement">{displacement(slice.displacement)}</dd>
              </div>
            </dl>
            <p data-field="sample">
              {slice.accepted} accepted / {slice.rejected} rejected · {slice.sample} eligible
              decisions · acceptance{" "}
              {slice.rate === null ? "unknown (no denominator)" : percent(slice.rate)}
              {" · "}
              {slice.evidence} evidence · damping {percent(slice.damping)}
            </p>
            <p data-field="pin-state">
              {plan.edits.some((edit) => edit.activity === slice.activity)
                ? "Pinned by owner"
                : "Not pinned"}
            </p>
            {editor?.(slice)}
          </Stack>
        ))}
      </div>
      <details>
        <summary>Feedback eligibility, exclusions and provenance</summary>
        <p>
          One logical proposal root per outcome; repeated rulings and revisions do not add
          independent successes. Pending, deferred and administrative work is not rejection.
          Complete outcome and exclusion counts are computed over the full basis, independently of
          the bounded provenance examples below.
        </p>
        <p data-field="allocation-coverage">
          {coverage.windowRulings} rulings in the seven-day window; {coverage.historicalRulings}{" "}
          earlier, {coverage.futureRulings} future and {coverage.invalidTimestampRulings} with
          invalid dates. {coverage.candidateRoots} candidate roots; {coverage.supersededRulings}{" "}
          superseded or repeated rulings.
        </p>
        <dl className="plugin-atyrode_babel_watch__allocation-figures">
          {ALLOCATION_EXCLUSIONS.map((reason) => (
            <div key={reason} data-exclusion={reason}>
              <dt>{FEEDBACK_LABEL[reason]}</dt>
              <dd>{exclusions.get(reason) ?? 0}</dd>
            </div>
          ))}
        </dl>
        <p data-field="allocation-provenance">
          {coverage.provenanceReturned} provenance examples shown (limit {coverage.provenanceLimit}
          ); {coverage.provenanceOmitted} omitted. The outcome totals above include omitted
          examples.
        </p>
        {plan.snapshot.feedback.length === 0 ? (
          <p>
            No provenance examples in this basis; counts above still cover every eligible root.
            Missing samples are not rejection.
          </p>
        ) : (
          <ul className="plugin-atyrode_babel_watch__allocation-provenance">
            {plan.snapshot.feedback.map((row) => (
              <li key={row.rootId}>
                <code>{row.recordId}</code> (root <code>{row.rootId}</code>): {row.decision},{" "}
                {row.excluded} · <time dateTime={row.at}>{row.at}</time> · operator{" "}
                {row.operatorId || "unknown"} · ruling <code>{row.rulingId}</code> · producing run{" "}
                {row.runId ?? "unknown"} · activity {row.activity ?? "unknown"} · attribution{" "}
                {row.attribution ?? "unknown"} · {row.supersededDecisions} superseded decisions
              </li>
            ))}
          </ul>
        )}
      </details>
      <details>
        <summary>Counted inventories and gaps (not a value score)</summary>
        <ul>
          {plan.snapshot.inventories.map((inventory) => (
            <li key={inventory.key} data-inventory={inventory.key}>
              <strong>
                {inventory.key}: {inventory.count === null ? "unknown" : inventory.count}
              </strong>
              {" — "}
              {inventory.meaning} · source: {inventory.source}
            </li>
          ))}
        </ul>
        <p>Self-generated backlog earns no allocation. Unknown inventory is not zero.</p>
      </details>
      <details>
        <summary>Authority snapshot and replay basis</summary>
        <p>
          Basis digest <code>{plan.basis.digest}</code> · watermark{" "}
          <code>{plan.snapshot.watermark}</code>
        </p>
        <p>This is the unchanged policy input, not a policy submission or execution permission.</p>
        <pre>{JSON.stringify(plan.snapshot.policy, null, 2)}</pre>
      </details>
      <ul className="plugin-atyrode_babel_watch__muted">
        {plan.caveats.map((caveat) => (
          <li key={caveat}>{caveat}</li>
        ))}
      </ul>
    </Stack>
  );
}

/** On-demand owner reads: Watch's polling never replaces a proposal or a typed edit. */
export function Allocation({ host }: { readonly host: HostServices }) {
  const [preview, setPreview] = useState<AllocationPreview | null>(null);
  const [drafts, setDrafts] = useState<Partial<Record<Activity, string>>>({});
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");
  const [versionName, setVersionName] = useState("");
  const [saved, setSaved] = useState<AllocationVersion | null>(null);
  const [readNote, setReadNote] = useState("");
  const [note, setNote] = useState("");
  const [pending, setPending] = useState("");
  const [savedBasis, setSavedBasis] = useState(false);
  const request = useRef(0);
  const busy = useRef(false);
  useEffect(
    () => () => {
      request.current += 1;
    },
    [],
  );

  function begin(label: string) {
    busy.current = true;
    setPending(label);
    return ++request.current;
  }
  function finish(id: number) {
    if (id !== request.current) return false;
    busy.current = false;
    setPending("");
    return true;
  }

  async function compute(input: AllocationPreviewInput, pinned?: Activity, clear = false) {
    if (busy.current) return;
    const id = begin("preview");
    setNote("");
    const outcome = await act(
      host,
      ACTIONS.previewAllocation,
      AllocationPreviewInputSchema.parse(input),
      AllocationPreviewSchema,
    );
    if (!finish(id)) return;
    if (!outcome.ok) {
      setNote(
        `Preview refused or unavailable: ${outcome.message}. The last successful plan is unchanged.`,
      );
      return;
    }
    setPreview(outcome.value);
    setSavedBasis(false);
    if (clear) setDrafts({});
    else if (pinned !== undefined)
      setDrafts((held) => {
        const next = { ...held };
        delete next[pinned];
        return next;
      });
    setNote("Preview ready. Nothing was applied or launched.");
  }

  async function save() {
    if (preview === null || busy.current || savedBasis || Object.keys(drafts).length > 0) return;
    const input = SaveAllocationInputSchema.safeParse({
      name,
      reason,
      basis: preview.basis,
      edits: preview.edits,
    });
    if (!input.success) {
      setNote(
        "A named version (up to 120 characters) and reason (up to 2000 characters) are required.",
      );
      return;
    }
    const id = begin("save");
    setNote("");
    const outcome = await act(host, ACTIONS.saveAllocation, input.data, AllocationVersionSchema);
    if (!finish(id)) return;
    if (!outcome.ok) {
      setNote(
        `Save refused or unavailable: ${outcome.message}. No new version was confirmed; refresh the basis if it moved.`,
      );
      return;
    }
    setSaved(outcome.value);
    setVersionName(outcome.value.version);
    setReadNote("");
    setSavedBasis(true);
    setNote(
      `Saved ${outcome.value.version} as a proposal only. No work or policy change was authorized. Refresh the basis before another save.`,
    );
  }

  async function load(version?: string) {
    if (busy.current) return;
    const id = begin("read");
    setReadNote("");
    const outcome = await act(
      host,
      ACTIONS.allocationVersion,
      AllocationVersionInputSchema.parse(version === undefined ? {} : { version }),
      AllocationVersionResultSchema,
    );
    if (!finish(id)) return;
    setSaved(outcome.ok ? outcome.value : null);
    setReadNote(
      outcome.ok
        ? outcome.value === null
          ? `No saved allocation ${version === undefined ? "version exists yet" : `named ${version}`}.`
          : ""
        : `Saved version unavailable: ${outcome.message}`,
    );
  }

  return (
    <Stack
      gap="var(--babel-space-3)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__allocation"
      role="region"
      aria-label="Allocation proposal"
      aria-busy={pending !== ""}
    >
      <h2 className="plugin-atyrode_babel_watch__title">Allocation proposal</h2>
      <p className="plugin-atyrode_babel_watch__lede">
        Owner-only arithmetic over an already-authorized budget. Preview and save do not start jobs,
        call models, enable duties or change ceilings, focus or disclosure. A saved intention is not
        execution authorization and is not installed into the scheduler.
      </p>
      <Cluster gap="var(--babel-space-2)">
        <button
          type="button"
          className="plugin-atyrode_babel_watch__quiet"
          data-action={door(ACTIONS.previewAllocation)}
          disabled={pending !== ""}
          onClick={() => void compute({ edits: preview?.edits ?? [] })}
        >
          {pending === "preview"
            ? "Computing…"
            : preview === null
              ? "Preview allocation"
              : "Refresh basis (keep pins)"}
        </button>
        {preview !== null && (preview.edits.length > 0 || Object.keys(drafts).length > 0) ? (
          <button
            type="button"
            className="plugin-atyrode_babel_watch__quiet"
            data-action="allocation-reset"
            disabled={pending !== ""}
            onClick={() => void compute({ edits: [] }, undefined, true)}
          >
            New preview without edits
          </button>
        ) : null}
      </Cluster>
      <p role="status" className="plugin-atyrode_babel_watch__note">
        {pending === ""
          ? note
          : `${pending === "save" ? "Saving intention" : pending === "read" ? "Reading saved version" : "Computing preview"}…`}
      </p>
      {preview === null ? (
        <p>No allocation preview read yet. No spend is authorized by opening this section.</p>
      ) : (
        <Stack gap="var(--babel-space-3)" data-field="allocation-preview">
          <h3>{savedBasis ? "Saved preview · refresh before editing" : "Unsaved proposal"}</h3>
          <p>
            Edit a total spend fraction between its protected floor and 1, then pin it. The baseline
            door renormalizes only the permitted remainder, keeping every earlier pin. An impossible
            split is refused, never repaired by raising a ceiling.
          </p>
          <Plan
            plan={preview}
            editor={(slice) => (
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  const value = drafts[slice.activity] ?? String(slice.proposed);
                  const fraction = Number(value);
                  if (
                    value.trim() === "" ||
                    !Number.isFinite(fraction) ||
                    fraction < slice.protected ||
                    fraction > 1 ||
                    !slice.enabled
                  )
                    return;
                  void compute(
                    {
                      basis: preview.basis,
                      edits: [
                        ...preview.edits.filter((edit) => edit.activity !== slice.activity),
                        { activity: slice.activity, fraction },
                      ],
                    },
                    slice.activity,
                  );
                }}
              >
                <label>
                  {slice.activity} fraction (0–1){" "}
                  <input
                    type="number"
                    className="plugin-atyrode_babel_watch__knob-input"
                    data-field="fraction"
                    min={slice.protected}
                    max={1}
                    step="any"
                    required
                    disabled={!slice.enabled || pending !== "" || savedBasis}
                    value={drafts[slice.activity] ?? String(slice.proposed)}
                    onInput={(event) => {
                      const value = event.currentTarget.value;
                      setDrafts((held) => ({ ...held, [slice.activity]: value }));
                    }}
                  />
                </label>{" "}
                <button
                  type="submit"
                  className="plugin-atyrode_babel_watch__quiet"
                  disabled={!slice.enabled || pending !== "" || savedBasis}
                >
                  Pin {slice.activity}
                </button>
                {preview.edits.some((edit) => edit.activity === slice.activity) ? (
                  <button
                    type="button"
                    className="plugin-atyrode_babel_watch__quiet"
                    disabled={pending !== "" || savedBasis}
                    onClick={() =>
                      void compute(
                        {
                          basis: preview.basis,
                          edits: preview.edits.filter((edit) => edit.activity !== slice.activity),
                        },
                        slice.activity,
                      )
                    }
                  >
                    Unpin {slice.activity}
                  </button>
                ) : null}
              </form>
            )}
          />
          {Object.keys(drafts).length === 0 ? null : (
            <p role="status">
              Unpreviewed values: pin each typed slice or{" "}
              <button
                type="button"
                className="plugin-atyrode_babel_watch__quiet"
                disabled={pending !== ""}
                onClick={() => setDrafts({})}
              >
                discard typed values
              </button>{" "}
              before saving. The figures above remain the last successful preview.
            </p>
          )}
          <form
            className="plugin-atyrode_babel_watch__allocation-save"
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <label>
              Version name
              <input
                className="plugin-atyrode_babel_watch__field"
                data-field="allocation-name"
                value={name}
                maxLength={120}
                required
                disabled={pending !== ""}
                onInput={(event) => setName(event.currentTarget.value)}
              />
            </label>
            <label>
              Reason
              <textarea
                className="plugin-atyrode_babel_watch__field"
                data-field="allocation-reason"
                value={reason}
                maxLength={2000}
                required
                disabled={pending !== ""}
                onInput={(event) => setReason(event.currentTarget.value)}
              />
            </label>
            <button
              type="submit"
              className="plugin-atyrode_babel_watch__primary"
              data-action={door(ACTIONS.saveAllocation)}
              disabled={
                pending !== "" ||
                savedBasis ||
                Object.keys(drafts).length > 0 ||
                name.trim() === "" ||
                reason.trim() === ""
              }
            >
              Save named intention
            </button>
          </form>
          <p>
            Destination: Babel&apos;s immutable allocation versions, not policy settings. Saving
            grants no execution authority.
          </p>
        </Stack>
      )}
      <Stack gap="var(--babel-space-3)">
        <h3>Read a saved intention</h3>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (versionName.trim() !== "") void load(versionName.trim());
          }}
        >
          <label>
            Saved version name{" "}
            <input
              className="plugin-atyrode_babel_watch__field"
              data-field="allocation-version"
              value={versionName}
              maxLength={120}
              disabled={pending !== ""}
              onInput={(event) => setVersionName(event.currentTarget.value)}
            />
          </label>{" "}
          <button
            type="submit"
            className="plugin-atyrode_babel_watch__quiet"
            disabled={pending !== "" || versionName.trim() === ""}
            data-action="allocation-read-named"
          >
            Read named version
          </button>
          <button
            type="button"
            className="plugin-atyrode_babel_watch__quiet"
            disabled={pending !== ""}
            data-action="allocation-read-latest"
            onClick={() => void load()}
          >
            Read latest version
          </button>
        </form>
        <p role="status">{readNote}</p>
        {saved === null ? null : (
          <Stack gap="var(--babel-space-3)" data-field="allocation-saved">
            <h3>
              {saved.version} · saved intention #{saved.seq}
            </h3>
            <p>
              Recorded by {saved.actorId} at{" "}
              <time dateTime={saved.recordedAt}>{saved.recordedAt}</time>: {saved.reason}
            </p>
            <p>
              Read-only replay of the saved basis and pins, not a fresh proposal against
              today&apos;s inputs. It authorizes no work.
            </p>
            <Plan plan={saved.plan} />
          </Stack>
        )}
      </Stack>
    </Stack>
  );
}
