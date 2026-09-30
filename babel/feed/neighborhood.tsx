import { useState, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Disclosure, Stack } from "@manifold/ui";
import {
  ACTIONS,
  EntityIdSchema,
  RecordIdSchema,
  RecordKindSchema,
  type NeighborhoodResult,
  type NeighborhoodSummary,
  type NeighborhoodSourceResult,
} from "../contract.ts";
import { BABEL_NODE, NO_SEAT, ask, openEntity, openRecord, refusal } from "./api.ts";
import { RAIL_POLL_MS } from "./rail.tsx";

type Status = NeighborhoodResult["facts"][number]["status"];
type Link = NeighborhoodResult["links"][number];

/** One read, not a browser walk. The server owns reachability, depth and every bound. */
export function Neighborhood({
  host,
  entityId,
  returnToEntityId,
}: {
  host: HostServices;
  entityId: string;
  returnToEntityId?: unknown;
}): ReactElement {
  const [failure, setFailure] = useState("");
  const [navigation, setNavigation] = useState("");
  const previous =
    typeof returnToEntityId === "string" &&
    returnToEntityId !== entityId &&
    EntityIdSchema.safeParse(returnToEntityId).success
      ? returnToEntityId
      : null;
  const read = usePolledResource<NeighborhoodResult | null>(
    async () => ask(host, ACTIONS.neighborhood, { entityId }),
    RAIL_POLL_MS,
    {
      key: "atyrode.babel.neighborhood",
      restartKey: entityId,
      initial: null,
      topics: [BABEL_NODE],
      events: host.client,
      onError: (reason) => setFailure(refusal(reason)),
      onSuccess: () => setFailure(""),
    },
  );
  const result = read.value?.entityId === entityId ? read.value : null;
  return (
    <section className="babel-neighborhood" aria-label="Entity neighbourhood">
      <h2>Stored neighbourhood</h2>
      {previous !== null && (
        <nav aria-label="Neighbourhood back navigation">
          <button
            type="button"
            className="babel-link"
            onClick={() => setNavigation(openEntity(host, previous) === "no_tile" ? NO_SEAT : "")}
          >
            Back to {previous}
          </button>
        </nav>
      )}
      {navigation !== "" && <p role="status">{navigation}</p>}
      {failure !== "" ? (
        <div role="status">
          <p>Neighbourhood unavailable: {failure}</p>
          <p>Its contents and coverage are unknown, not empty.</p>
          <button type="button" onClick={() => read.refresh()}>
            Retry neighbourhood read
          </button>
        </div>
      ) : result === null ? (
        <p role="status">Reading stored neighbourhood…</p>
      ) : (
        <NeighborhoodContents host={host} result={result} />
      )}
    </section>
  );
}

function StoredStatus({
  status,
  label = "Status",
}: {
  status: Status;
  label?: string;
}): ReactElement {
  return (
    <p className="babel-neighborhood-status" data-state={status?.state ?? "unknown"}>
      {label}: <strong>{status?.state ?? "unknown (no stored event)"}</strong>
      {status !== null && (
        <>
          {" · "}
          {status.actorKind ?? "unknown actor kind"} {status.actorId ?? "unknown actor"}
          {" · "}
          <time dateTime={status.at}>{status.at}</time>
          {" · event "}
          {status.id}
          {" · sequence "}
          {status.seq}
          {status.runId !== null && <> · run {status.runId}</>}
          {status.reason !== null && <> · stored reason: {status.reason}</>}
        </>
      )}
    </p>
  );
}

function Payload({ json }: { json: string }): ReactElement {
  const [open, setOpen] = useState(false);
  return (
    <Disclosure
      header="Stored payload JSON (not an archive quotation)"
      open={open}
      onOpenChange={setOpen}
    >
      <pre className="babel-neighborhood-payload">{json}</pre>
    </Disclosure>
  );
}

function Coverage({ result }: { result: NeighborhoodResult }): ReactElement {
  const { coverage, limits } = result;
  const reasons: Record<NeighborhoodResult["coverage"]["reasons"][number], string> = {
    depth: "Depth limit reached",
    nodes: "Node limit reached",
    items: "Item limit reached",
    bytes: "Byte limit reached",
    unavailable: "Entity material unavailable",
  };
  return (
    <div className="babel-neighborhood-coverage" role="status">
      <strong>{coverage.truncated ? "Partial neighbourhood" : "Bounded neighbourhood read"}</strong>
      <p>
        Covered scope: {coverage.scope}. This is not a complete project inventory or a generated
        summary. Only stored, linked material is considered.
      </p>
      <p>
        Containment traversal:{" "}
        {coverage.traversalComplete ? "exhausted within this scope" : "incomplete"}. Stored
        material:{" "}
        {coverage.recordsComplete ? "all eligible rows in this scope returned" : "incomplete"}.
      </p>
      {coverage.reasons.length > 0 && (
        <ul>
          {coverage.reasons.map((reason) => (
            <li key={reason}>{reasons[reason]}</li>
          ))}
        </ul>
      )}
      <p>
        Applied limits: depth {limits.depth} · {limits.maxNodes} nodes · {limits.maxItems} items ·{" "}
        {limits.maxBytes.toLocaleString()} bytes.
      </p>
      <p>
        {result.nodes.length} entities returned · {coverage.visitedNodes} visited ·{" "}
        {coverage.returnedItems} material rows returned · {coverage.omittedItems} material rows
        omitted for returned entities · at least {coverage.omittedNodesAtLeast} nodes omitted at the
        visited frontier (not a census of unseen descendants) · {coverage.unavailableEntities}{" "}
        unavailable entities · {coverage.resultBytes.toLocaleString()} response bytes.
      </p>
      <p>
        Archive access: unknown; not probed. Unreviewed material: unknown; a catalog entry or a
        missing citation does not establish review history.
      </p>
    </div>
  );
}

function NavigationSummary({
  host,
  summary,
}: {
  host: HostServices;
  summary: NeighborhoodSummary;
}): ReactElement {
  const [sources, setSources] = useState<NeighborhoodSourceResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState("");
  const [navigation, setNavigation] = useState("");
  const [offset, setOffset] = useState(0);
  const source = summary.source;
  async function openSources(next: number): Promise<void> {
    if (!source) return;
    setOffset(next);
    setSources(null);
    setFailure("");
    setLoading(true);
    try {
      setSources(
        await ask(host, ACTIONS.neighborhoodSource, {
          sourceId: source.id,
          offset: next,
        }),
      );
    } catch (error) {
      setFailure(refusal(error));
    } finally {
      setLoading(false);
    }
  }
  return (
    <section aria-label="Generated navigation summaries">
      <h3>Generated navigation summaries</h3>
      <p>
        <strong>Inference for navigation, not evidence or authoritative facts.</strong> Summarizing
        creates no entities or relationships. Reads never authorize generation or paid work.
      </p>
      <p role="status">
        {summary.state === "stale"
          ? "Outdated or withheld summary: source revisions, inputs or review changed."
          : summary.state === "available"
            ? "Generated summary available for this bounded revision."
            : summary.state === "bounded"
              ? "Summary omitted to preserve the response byte bound."
              : summary.state === "unavailable"
                ? "Summary unavailable. Stored records remain readable below."
                : "No generated summary for this scope yet. Stored records remain readable below."}
      </p>
      {source && (
        <>
          <p>
            Hub-record source revision <code>{source.revision}</code> · captured {source.capturedAt}{" "}
            · {source.disputed} disputed inputs · {source.stale} stale/replaced inputs ·{" "}
            {source.redactions} mandatory redactions.
          </p>
          <p>
            Summary input scope: {source.coverage.scope}; depth {source.query.depth},{" "}
            {source.query.maxNodes} nodes, {source.query.maxItems} items, {source.query.maxBytes}{" "}
            bytes.{" "}
            {source.coverage.recordsComplete
              ? "All eligible stored rows in this bounded scope."
              : "Incomplete source coverage — not a whole-project summary."}{" "}
            {source.coverage.omittedItems} omitted material rows; at least{" "}
            {source.coverage.omittedNodesAtLeast} omitted frontier nodes;{" "}
            {source.coverage.unavailableEntities} unavailable entities. Limits reached:{" "}
            {source.coverage.reasons.join(", ") || "none"}.
          </p>
        </>
      )}
      {summary.coverage && (
        <p>
          Mapping coverage:{" "}
          {summary.coverage.partial ? "incomplete" : "complete for the supplied snapshot"} ·{" "}
          {summary.coverage.summarizedBytes} summarized bytes · {summary.coverage.unmappedBytes}{" "}
          unmapped bytes · {summary.coverage.gapBytes} gap bytes · {summary.omittedViews} additional
          summary views omitted.
        </p>
      )}
      {summary.producer && (
        <p className="babel-neighborhood-provenance">
          Version {summary.versionId} · producing contract {summary.producer.contractDigest} ·{" "}
          configured source route {summary.producer.sourceMachineId} · executor{" "}
          {summary.producer.executorMachineId} · profile {summary.producer.profile.containerId}@
          {summary.producer.profile.expectedRevision}
        </p>
      )}
      {summary.views.map((view) => (
        <article key={view.summary.id}>
          <h4>
            Navigation inference · records {view.node.span.firstRecord}–{view.node.span.lastRecord}
          </h4>
          <p>{view.summary.text}</p>
          <p className="babel-neighborhood-provenance">
            Summary {view.summary.id} · run {view.summary.runId} · recipe {view.summary.recipeId}@
            {view.summary.recipeVersion} · {view.summary.createdAt} · span digest{" "}
            {view.node.span.digest}
          </p>
          <button
            type="button"
            disabled={loading}
            onClick={() => void openSources(view.node.span.firstRecord - 1)}
          >
            Open exact source records {view.node.span.firstRecord}–{view.node.span.lastRecord}
          </button>
        </article>
      ))}
      {loading && <p role="status">Reading exact retained source rows…</p>}
      {failure && (
        <div role="status">
          <p>Source read unavailable: {failure}</p>
          <button type="button" onClick={() => void openSources(offset)}>
            Retry exact source read
          </button>
        </div>
      )}
      {sources && (
        <section aria-label="Exact summary source records">
          <h4>Exact retained source records</h4>
          <p>
            Mandatory-redacted stored rows, not generated prose or archive quotations. Revision
            digests identify the original rows, including status and attribution; catalog locators
            remain current-catalog metadata, not historical citation proof. Opening a record or
            entity below opens its current panel; the exact historical input is displayed here.
          </p>
          {sources.source === null && <p role="status">This source snapshot is unavailable.</p>}
          {sources.rows.map((row) => (
            <div key={row.record}>
              <p>
                Source record {row.record} · {row.source.kind} · {row.source.id} · revision{" "}
                <code>{row.source.revision}</code>
              </p>
              {(row.source.kind === "records" || row.source.kind === "questions") &&
                RecordIdSchema.safeParse(row.source.id).success && (
                  <button
                    type="button"
                    onClick={() =>
                      setNavigation(openRecord(host, row.source.id) === "no_tile" ? NO_SEAT : "")
                    }
                  >
                    Open record {row.source.id}
                  </button>
                )}
              {row.source.kind === "nodes" && EntityIdSchema.safeParse(row.source.id).success && (
                <button
                  type="button"
                  onClick={() =>
                    setNavigation(
                      openEntity(host, row.source.id, source?.query.entityId) === "no_tile"
                        ? NO_SEAT
                        : "",
                    )
                  }
                >
                  Open entity {row.source.id}
                </button>
              )}
              <Payload json={JSON.stringify(row.source.value)} />
            </div>
          ))}
          {sources.omittedRecords > 0 && (
            <p role="status">
              {sources.omittedRecords} source records omitted whole by this page's byte bound.
            </p>
          )}
          {navigation && <p role="status">{navigation}</p>}
          {offset > 0 && (
            <button
              type="button"
              disabled={loading}
              onClick={() => void openSources(Math.max(0, offset - 16))}
            >
              Previous source records
            </button>
          )}
          {sources.nextOffset !== null && (
            <button
              type="button"
              disabled={loading}
              onClick={() => void openSources(sources.nextOffset!)}
            >
              Next source records
            </button>
          )}
          <button type="button" onClick={() => setSources(null)}>
            Close exact source records
          </button>
        </section>
      )}
    </section>
  );
}

function NeighborhoodContents({
  host,
  result,
}: {
  host: HostServices;
  result: NeighborhoodResult;
}): ReactElement {
  const [navigation, setNavigation] = useState("");
  const nodes = new Map(result.nodes.map((node) => [node.id, node]));
  const containment: Link[] = [];
  const contradictions: Link[] = [];
  const relations: Link[] = [];
  for (const link of result.links) {
    if (link.parentId !== null && link.childId !== null) containment.push(link);
    else if (link.kind === "contradicts") contradictions.push(link);
    else relations.push(link);
  }

  function entity(id: string): ReactElement {
    const node = nodes.get(id);
    if (!EntityIdSchema.safeParse(id).success) return <span>{id} (unnameable entity)</span>;
    return (
      <button
        type="button"
        className="babel-link"
        data-entity={id}
        onClick={() =>
          setNavigation(
            openEntity(host, id, id === result.entityId ? undefined : result.entityId) === "no_tile"
              ? NO_SEAT
              : "",
          )
        }
      >
        {node === undefined ? `${id} (not returned in this scope)` : `${node.name} · ${id}`}
      </button>
    );
  }

  function record(id: string, title = id): ReactElement {
    if (!RecordIdSchema.safeParse(id).success) return <span>{id} (unnameable record)</span>;
    return (
      <button
        type="button"
        className="babel-link"
        data-record={id}
        onClick={() => setNavigation(openRecord(host, id) === "no_tile" ? NO_SEAT : "")}
      >
        Open record: {title}
      </button>
    );
  }

  function endpoint(kind: string, id: string): ReactElement {
    if (kind === "entity") return entity(id);
    if (kind === "record" || kind === "question" || RecordKindSchema.safeParse(kind).success)
      return record(id);
    return (
      <span>
        {kind}: {id}
      </span>
    );
  }

  function linkRows(links: readonly Link[]): ReactElement {
    return (
      <ul className="babel-neighborhood-list">
        {links.map((link) => (
          <li key={link.id} data-relation={link.kind}>
            {link.parentId !== null && link.childId !== null ? (
              <p>
                Parent {entity(link.parentId)} <span aria-hidden="true">→</span> child{" "}
                {entity(link.childId)} · {link.kind}
              </p>
            ) : (
              <p>
                <strong>{link.kind}</strong>
              </p>
            )}
            <p>
              Stored direction: {endpoint(link.fromKind, link.fromId)}{" "}
              <span aria-label="to">→</span> {endpoint(link.toKind, link.toId)}
            </p>
            <p className="babel-neighborhood-provenance">
              Link {link.id} · {link.actorKind} {link.actorId} ·{" "}
              <time dateTime={link.createdAt}>{link.createdAt}</time>
              {link.position !== null && <> · position {link.position}</>}
            </p>
            {link.note !== null && <p>Stored link note (not an archive quotation): {link.note}</p>}
          </li>
        ))}
      </ul>
    );
  }

  const empty =
    result.state === "found" &&
    result.coverage.recordsComplete &&
    result.coverage.returnedItems === 0;
  return (
    <Stack gap="var(--babel-space-3)">
      <Coverage result={result} />
      <NavigationSummary
        key={result.summary.source?.id ?? result.summary.state}
        host={host}
        summary={result.summary}
      />
      {navigation !== "" && <p role="status">{navigation}</p>}
      {result.state === "missing" ? (
        <p className="babel-neighborhood-missing">
          Entity {result.entityId} is missing from this read. This is not an empty existing entity.
        </p>
      ) : (
        <>
          {empty && (
            <p>
              No stored material in the covered scope. The entity exists; this is an empty
              neighbourhood, not a missing entity.
            </p>
          )}
          <section aria-label="Containment spine">
            <h3>Containment spine</h3>
            <p>
              Each returned entity appears once below, at the depth supplied by the read. Shared
              descendants and cycles remain links, not repeatedly expanded branches. Only
              containment extends this scope; other relationships do not.
            </p>
            <ul className="babel-neighborhood-list babel-neighborhood-entities">
              {result.nodes.map((node) => (
                <li key={node.id} data-node={node.id}>
                  {entity(node.id)} · {node.kind} · depth {node.depth}
                  <p className="babel-neighborhood-provenance">
                    Canonical ID {node.canonicalId} · created by {node.createdBy} ·{" "}
                    <time dateTime={node.createdAt}>{node.createdAt}</time>
                  </p>
                </li>
              ))}
            </ul>
            {containment.length === 0 ? (
              <p>No containment links returned in this scope.</p>
            ) : (
              linkRows(containment)
            )}
          </section>
          <section aria-label="Stored facts">
            <h3>Stored facts</h3>
            <p>Ledger values and their recorded authority, not newly verified claims.</p>
            {result.facts.length === 0 && <p>No facts returned in this scope.</p>}
            <ul className="babel-neighborhood-list">
              {result.facts.map((fact) => (
                <li key={fact.id} data-fact={fact.id}>
                  <p>
                    {entity(fact.entityId)} · <strong>{fact.predicate}</strong>: {fact.value}
                  </p>
                  {fact.objectId !== null && <p>Object entity: {entity(fact.objectId)}</p>}
                  <p>
                    Authority: {fact.authorityKind} {fact.authorityId} · confidence:{" "}
                    {fact.confidence}
                  </p>
                  <StoredStatus status={fact.status} />
                  <p className="babel-neighborhood-provenance">
                    Fact {fact.id} · observed {fact.observedAt} · recorded {fact.recordedAt} · valid
                    from {fact.validFrom} · valid until {fact.validUntil ?? "not recorded"} ·
                    supersedes {fact.supersedesId ?? "none recorded"} · replaced by{" "}
                    {fact.replacedBy ?? "none recorded"}
                  </p>
                  {fact.note !== null && (
                    <p>Stored fact note (not an archive quotation): {fact.note}</p>
                  )}
                </li>
              ))}
            </ul>
          </section>
          <section aria-label="Filed records">
            <h3>Filed records</h3>
            <p>
              Babel conclusions remain reviewable records, not ledger authority or archive
              quotations.
            </p>
            {result.records.length === 0 && <p>No records returned in this scope.</p>}
            <ul className="babel-neighborhood-list">
              {result.records.map((item) => (
                <li key={item.id} data-filed-record={item.id}>
                  <p>
                    {record(item.id, item.title)} · {item.kind}
                  </p>
                  <StoredStatus status={item.status} />
                  <StoredStatus status={item.ruling} label="Operator ruling" />
                  <p className="babel-neighborhood-provenance">
                    Record {item.id} · {item.actorKind} {item.actorId} · created {item.createdAt} ·
                    run {item.runId ?? "unknown"} · recipe {item.recipeId ?? "unknown"}@
                    {item.recipeVersion ?? "unknown"}
                    {" · sequence "}
                    {item.seq}
                    {" · root "}
                    {item.rootId}
                    {" · parent "}
                    {item.parentId ?? "none recorded"}
                    {" · supersedes "}
                    {item.supersedesId ?? "none recorded"}
                    {" · replaced by "}
                    {item.replacedBy ?? "none recorded"}
                  </p>
                  <Payload json={item.payloadJson} />
                </li>
              ))}
            </ul>
            <h4>Filing provenance</h4>
            {result.filings.length === 0 && (
              <p>
                No filing rows returned in this scope; item bounds may omit provenance independently
                of records.
              </p>
            )}
            <ul className="babel-neighborhood-list">
              {result.filings.map((filing) => (
                <li key={filing.id}>
                  <p>
                    {record(filing.recordId)} · filed under {entity(filing.entityId)}
                  </p>
                  <p>Stored rationale: {filing.rationale}</p>
                  <p className="babel-neighborhood-provenance">
                    Filing {filing.id} · {filing.authorKind} {filing.authorId} · {filing.createdAt}{" "}
                    · {filing.heuristic ? "heuristic filing" : "not a heuristic filing"}
                  </p>
                </li>
              ))}
            </ul>
          </section>
          <section aria-label="Questions and answers">
            <h3>Questions and answers</h3>
            {result.questions.length === 0 && <p>No questions returned in this scope.</p>}
            <ul className="babel-neighborhood-list">
              {result.questions.map((question) => (
                <li key={question.id}>
                  <p>{record(question.id, question.text)}</p>
                  <p>Stored reason: {question.why}</p>
                  <p className="babel-neighborhood-provenance">
                    {question.kind} · {question.class} · raised by {question.raisedByKind}{" "}
                    {question.raisedById} · {question.createdAt}
                  </p>
                  {question.status === null ? (
                    <p className="babel-neighborhood-status" data-state={question.effectiveState}>
                      Status: <strong>{question.effectiveState}</strong> · no status event recorded
                    </p>
                  ) : (
                    <StoredStatus status={question.status} />
                  )}
                  <Payload json={question.payloadJson} />
                </li>
              ))}
            </ul>
            <h4>Stored answers</h4>
            {result.answers.length === 0 && (
              <p>
                No answers returned in this scope. An omitted answer is not proof that a question is
                unanswered.
              </p>
            )}
            <ul className="babel-neighborhood-list">
              {result.answers.map((answer) => (
                <li key={answer.id}>
                  <p>
                    Answer to {record(answer.questionId)}: {answer.text}
                  </p>
                  <p className="babel-neighborhood-provenance">
                    Answer {answer.id} · {answer.actorId} · outcome {answer.outcome} ·{" "}
                    {answer.recordedAt}
                  </p>
                </li>
              ))}
            </ul>
          </section>
          <section aria-label="Contradictions">
            <h3>Contradictions</h3>
            <p>
              Stored links, not a resolution of the disagreement. Their targets do not extend this
              read.
            </p>
            {contradictions.length === 0 ? (
              <p>No contradiction links returned in this scope.</p>
            ) : (
              linkRows(contradictions)
            )}
          </section>
          <section aria-label="Other relations">
            <h3>Other relations</h3>
            <p>
              Labelled endpoints only; following a link opens a separate view, never expands this
              projection.
            </p>
            {relations.length === 0 ? (
              <p>No other relations returned in this scope.</p>
            ) : (
              linkRows(relations)
            )}
          </section>
          <section aria-label="Current catalog sources">
            <h3>Current catalog sources</h3>
            <p>
              Current-catalog locators are NOT historical citation proof. They may identify a
              different capture from the one a record cited. Review history and archive access are
              unknown. Stored payloads and notes above are not presented as archive quotations.
            </p>
            {result.sources.length === 0 && (
              <p>No current-catalog sources returned in this scope.</p>
            )}
            <ul className="babel-neighborhood-list">
              {result.sources.map((source) => (
                <li key={source.selector}>
                  <p>
                    {source.title ?? "Untitled source"} · <code>{source.selector}</code>
                  </p>
                  <p>
                    Authority: {source.authority} · review state: {source.reviewState}
                  </p>
                  <p className="babel-neighborhood-provenance">
                    Repository identity {source.repositoryIdentity ?? "unknown"} · remote{" "}
                    {source.repositoryRemote ?? "unknown"} · snapshot{" "}
                    {source.snapshotId ?? "unknown"} · archive path{" "}
                    {source.archivePath ?? "unknown"} · archive label{" "}
                    {source.archiveLabel ?? "unknown"} · digest {source.contentDigest ?? "unknown"}{" "}
                    · modified {source.modifiedAt ?? "unknown"} · archived{" "}
                    {source.archivedAt ?? "unknown"}
                  </p>
                </li>
              ))}
            </ul>
          </section>
        </>
      )}
    </Stack>
  );
}
