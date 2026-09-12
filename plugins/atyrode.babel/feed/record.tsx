import { useEffect, useRef, useState, type FormEvent, type ReactElement } from "react";
import type { HostServices, PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, Disclosure, ScrollRegion, Stack } from "@manifold/ui";
import { ACTIONS, FEED_PLUGIN_ID } from "../contract.ts";
import {
  BABEL_NODE,
  ask,
  refusal,
  useSelection,
  type RecordPeel,
  type ThreadResult,
  type TopicsResult,
} from "./api.ts";
import { Peel } from "./peel.tsx";
import { RULE_KEYS, type ActedHandler } from "./rows.tsx";
import { Thread } from "./thread.tsx";

/*
  THE RECORD PANEL — §8.7's peek pane, as a panel.

  It draws whatever Home is looking at (`useSelection`): pressing ↵ on a row points the
  selection at that record and this panel follows, `j`/`k` walk it, and a record panel
  mounted with nothing selected says so rather than drawing an empty frame. A panel takes no
  argument in this revision (a tile leaf's panel ref is `{kind, panelId}` and `PanelProps`
  carries the host alone), so the plugin's own module is where "what is being read" lives.

  ITS KEYS ARE ITS OWN. Home owns the window's keyboard because Home is the list; this panel
  binds `y n d f q` on its own subtree, so the two never rule on two different records from
  one press.
*/

/** How often the record and its thread re-read when no event has arrived. */
const RECORD_POLL_MS = 20_000;

export function RecordPanel({ host }: PanelProps): ReactElement {
  const selection = useSelection();
  return (
    <ScrollRegion className={`plugin-${FEED_PLUGIN_ID.replaceAll(".", "_")}`} aria-label="Record">
      <Stack className="babel-panel" gap="var(--babel-space-4)">
        {selection.recordId === "" ? (
          <div className="babel-state">
            <strong>No record open</strong>
            <span>Press ↵ on a row in Home, or open one from its claim, and it is read here.</span>
          </div>
        ) : (
          <RecordView host={host} id={selection.recordId} />
        )}
      </Stack>
    </ScrollRegion>
  );
}

function RecordView({ host, id }: { host: HostServices; id: string }): ReactElement {
  const [announcement, setAnnouncement] = useState("");
  const [failure, setFailure] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const root = useRef<HTMLDivElement | null>(null);

  const peel = usePolledResource<RecordPeel | null>(async () => ask(host, ACTIONS.record, { id }), RECORD_POLL_MS, {
    key: "atyrode.babel.record",
    restartKey: id,
    initial: null,
    topics: [BABEL_NODE],
    events: host.client,
    onError: (reason) => setFailure(refusal(reason)),
    onSuccess: () => setFailure(""),
  });
  const thread = usePolledResource<ThreadResult | null>(async () => ask(host, ACTIONS.thread, { id }), RECORD_POLL_MS, {
    key: "atyrode.babel.thread",
    restartKey: id,
    initial: null,
    topics: [BABEL_NODE],
    events: host.client,
  });

  useEffect(() => setNow(Date.now()), [peel.value]);

  const acted: ActedHandler = (_act, _done, message) => {
    setAnnouncement(message);
    peel.refresh();
    thread.refresh();
  };

  const record = peel.value;
  return (
    <div
      className="babel-record-root"
      ref={root}
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.metaKey || event.ctrlKey || event.altKey) return;
        if (event.target instanceof HTMLElement && event.target.closest("input, textarea, select") !== null) return;
        const act = RULE_KEYS[event.key];
        if (act === undefined || record === null) return;
        const control = root.current?.querySelector<HTMLButtonElement>(`[data-ruling="${act}"]`);
        if (control === null || control === undefined) return;
        event.preventDefault();
        control.click();
      }}
    >
      <p className="babel-said" role="status" aria-live="polite">
        {announcement}
      </p>
      {failure !== "" && (
        <div className="babel-state" role="alert">
          <strong>The record could not be read.</strong>
          <span>{failure}</span>
          <button type="button" onClick={() => peel.refresh()}>
            Try again
          </button>
        </div>
      )}
      {record === null && failure === "" && <p className="babel-note">Reading the record…</p>}
      {record !== null && (
        <Stack gap="var(--babel-space-5)">
          <Peel host={host} peel={record} onActed={acted} now={now} />
          <FilingDesk
            host={host}
            id={id}
            filed={record.post.topics}
            onFiled={(message) => {
              setAnnouncement(message);
              peel.refresh();
            }}
          />
          <Thread host={host} id={id} thread={thread.value} onPosted={() => thread.refresh()} now={now} />
        </Stack>
      )}
    </div>
  );
}

/**
 * THE FILING DESK (§4.13). Filing is an append-only link carrying a rationale, so both acts
 * ask for words: the rationale says why this record is about that topic, and the withdrawal
 * says why it is not, and the doors refuse either without them. Filing does not CREATE a
 * topic — only the operator's acceptance mints an entity — so it is a list of the topics that
 * exist and never a text box that would mint one by typo.
 */
function FilingDesk({
  host,
  id,
  filed,
  onFiled,
}: {
  host: HostServices;
  id: string;
  filed: ReadonlyArray<{ readonly id: string; readonly name: string }>;
  onFiled: (message: string) => void;
}): ReactElement {
  const [open, setOpen] = useState(false);
  const [known, setKnown] = useState<TopicsResult | null>(null);
  const [entity, setEntity] = useState("");
  const [words, setWords] = useState("");
  const [withdrawing, setWithdrawing] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");

  // Opening the fold is what reads the topics, so the panel still fetches nothing on open.
  useEffect(() => {
    if (!open || known !== null) return;
    ask(host, ACTIONS.topics, {})
      .then(setKnown)
      .catch((reason: unknown) => setFailure(refusal(reason)));
  }, [host, known, open]);

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    const rationale = words.trim();
    const target = withdrawing === "" ? entity : withdrawing;
    if (target === "" || rationale === "") return;
    setWorking(true);
    setFailure("");
    try {
      if (withdrawing === "") {
        await ask(host, ACTIONS.file, { id, entity: target, rationale });
        onFiled(`Filed under ${target}. The link is appended and attributed to you.`);
      } else {
        await ask(host, ACTIONS.unfile, { id, entity: target, reason: rationale });
        onFiled(`Withdrawn from ${target}. The withdrawal is a row, not an absence.`);
      }
      setEntity("");
      setWords("");
      setWithdrawing("");
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  return (
    <Disclosure
      className="babel-filing"
      open={open}
      onOpenChange={setOpen}
      header={`Filed under ${filed.length === 0 ? "nothing" : filed.map((topic) => `t/${topic.name}`).join(" · ")}`}
    >
      <Stack gap="var(--babel-space-3)">
        <form onSubmit={(event) => void submit(event)}>
          <Stack gap="var(--babel-space-2)">
            <label>
              {withdrawing === "" ? "File it under" : `Withdraw it from t/${withdrawing}`}
              {withdrawing === "" && (
                <select value={entity} onInput={(event) => setEntity(event.currentTarget.value)}>
                  <option value="">Choose a topic</option>
                  {(known?.topics ?? []).map((topic) => (
                    <option value={topic.id} key={topic.id}>
                      t/{topic.name}
                    </option>
                  ))}
                </select>
              )}
            </label>
            <label>
              {withdrawing === "" ? "Why this record is about it (required)" : "Why it is not (required)"}
              <textarea value={words} rows={2} required onInput={(event) => setWords(event.currentTarget.value)} />
            </label>
            <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
              <button
                type="submit"
                className="babel-primary"
                disabled={working || words.trim() === "" || (withdrawing === "" && entity === "")}
              >
                {working ? "Recording…" : withdrawing === "" ? "File it" : "Withdraw it"}
              </button>
              {withdrawing !== "" && (
                <button type="button" onClick={() => setWithdrawing("")} disabled={working}>
                  Cancel
                </button>
              )}
            </Cluster>
          </Stack>
        </form>
        {filed.length > 0 && (
          <Cluster gap="var(--babel-space-2)">
            {filed.map((topic) => (
              <button
                type="button"
                className="babel-link"
                key={topic.id}
                data-unfile={topic.id}
                onClick={() => setWithdrawing(topic.id)}
              >
                withdraw t/{topic.name}
              </button>
            ))}
          </Cluster>
        )}
        {failure !== "" && (
          <p className="babel-error" role="alert">
            {failure}
          </p>
        )}
      </Stack>
    </Disclosure>
  );
}
