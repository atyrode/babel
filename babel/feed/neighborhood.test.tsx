import { beforeEach, describe, expect, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import {
  FEED_PLUGIN_ID,
  NeighborhoodQuerySchema,
  PANELS,
  TopicQuerySchema,
  type NeighborhoodResult,
} from "../contract.ts";
import { forgetSelection, look, looking, NO_SEAT } from "./api.ts";
import { Neighborhood } from "./neighborhood.tsx";
import { TopicPanel } from "./topic.tsx";
import { Denial, fakeHost, feed, mount, neighborhood, topic } from "./testing.tsx";

const ROOT = "ent_0000beef";
const LEFT = "ent_0000cafe";
const RIGHT = "ent_0000face";
const SHARED = "ent_0000abcd";
const OUTSIDE = "ent_0000dead";
const AT = "2026-09-10T00:00:00Z";

type Link = NeighborhoodResult["links"][number];

function link(id: string, parentId: string, childId: string, kind = "contains"): Link {
  return {
    id,
    kind,
    fromKind: "entity",
    fromId: kind === "part-of" ? childId : parentId,
    toKind: "entity",
    toId: kind === "part-of" ? parentId : childId,
    position: null,
    note: null,
    actorKind: "operator",
    actorId: "p1",
    createdAt: AT,
    parentId,
    childId,
  };
}

function stored(): NeighborhoodResult {
  const base = neighborhood();
  return neighborhood({
    nodes: [
      { ...base.nodes[0]!, id: ROOT, name: "babel", depth: 0 },
      { ...base.nodes[0]!, id: LEFT, name: "Empty service", depth: 1 },
      { ...base.nodes[0]!, id: RIGHT, name: "Machine", depth: 1 },
      { ...base.nodes[0]!, id: SHARED, name: "Shared child", depth: 2 },
    ],
    links: [
      link("part-left", ROOT, LEFT, "part-of"),
      link("contains-right", ROOT, RIGHT),
      link("left-shared", LEFT, SHARED),
      link("right-shared", RIGHT, SHARED),
      link("cycle", SHARED, ROOT),
      { ...link("dependency", ROOT, OUTSIDE), kind: "depends-on", parentId: null, childId: null },
      {
        ...link("contradiction", ROOT, OUTSIDE),
        kind: "contradicts",
        fromKind: "finding",
        fromId: "fnd_0000000b",
        toKind: "fact",
        toId: "fact-disputed",
        parentId: null,
        childId: null,
        note: "The newer observation conflicts with the proposal.",
      },
    ],
    facts: [
      {
        id: "fact-disputed",
        entityId: ROOT,
        predicate: "service-state",
        value: "offline",
        objectId: null,
        validFrom: AT,
        validUntil: "2026-09-11T00:00:00Z",
        observedAt: AT,
        authorityKind: "operator",
        authorityId: "p1",
        confidence: "asserted",
        note: "Stored note, not a transcript extract.",
        supersedesId: "fact-previous",
        replacedBy: "fact-replacement",
        recordedAt: AT,
        status: {
          id: "status-fact",
          seq: 2,
          state: "disputed",
          actorId: "reviewer-1",
          actorKind: "run",
          runId: "run-status",
          reason: "The service was seen online later.",
          at: "2026-09-12T00:00:00Z",
        },
      },
    ],
    records: [
      {
        id: "pro_0000000a",
        kind: "proposal",
        rootId: "pro_0000000a",
        supersedesId: null,
        replacedBy: "pro_0000000b",
        parentId: null,
        seq: 1,
        title: "Recheck the service",
        payloadJson: '{"note":"Stored model payload, not a copied archive quote"}',
        runId: "run-proposal",
        recipeId: "recipe-service",
        recipeVersion: 3,
        actorKind: "run",
        actorId: "run-proposal",
        createdAt: AT,
        status: {
          id: "status-record",
          seq: 3,
          state: "stale",
          actorId: "p1",
          actorKind: "operator",
          runId: null,
          reason: "The service has changed.",
          at: "2026-09-13T00:00:00Z",
        },
        ruling: null,
      },
    ],
    filings: [
      {
        id: "filing-1",
        recordId: "pro_0000000a",
        entityId: ROOT,
        rationale: "Repository identity only",
        authorKind: "system",
        authorId: "seed",
        heuristic: true,
        createdAt: AT,
      },
    ],
    questions: [
      {
        id: "qst_0000000c",
        kind: "clarification",
        class: "blocking",
        text: "Which machine owns the service?",
        why: "The recorded bindings disagree.",
        payloadJson: '{"entityId":"ent_0000beef"}',
        raisedByKind: "run",
        raisedById: "run-question",
        createdAt: AT,
        status: null,
        effectiveState: "open",
      },
    ],
    answers: [
      {
        id: "answer-1",
        questionId: "qst_0000000c",
        actorId: "p1",
        outcome: "answered",
        text: "Use the enrolled machine.",
        recordedAt: "2026-09-14T00:00:00Z",
      },
    ],
    sources: [
      {
        selector: "session:service",
        title: "Current capture",
        repositoryIdentity: "repo-service",
        repositoryRemote: "https://example.invalid/service",
        snapshotId: "snapshot-today",
        archivePath: "sessions/service.jsonl",
        archiveLabel: "synthetic",
        contentDigest: "digest-today",
        modifiedAt: AT,
        archivedAt: "2026-09-15T00:00:00Z",
        authority: "current-catalog",
        reviewState: "unknown",
      },
    ],
    coverage: { ...base.coverage, visitedNodes: 4, returnedItems: 13, resultBytes: 6000 },
  });
}

beforeEach(() => {
  resetPolledResources();
  forgetSelection();
});

describe("the shared neighbourhood projection", () => {
  test("navigates the direction-normalized spine, including a zero-post entity, without walking it again", async () => {
    const rich = stored();
    const left = rich.nodes.find((node) => node.id === LEFT)!;
    const fake = fakeHost({
      topic: (args) => {
        const id = TopicQuerySchema.parse(args).topic;
        return topic({
          topic: { ...topic().topic!, id, name: id === LEFT ? left.name : "babel", posts: 0 },
        });
      },
      neighborhood: (args) =>
        NeighborhoodQuerySchema.parse(args).entityId === LEFT
          ? neighborhood({ entityId: LEFT, nodes: [{ ...left, depth: 0 }] })
          : rich,
      feed: () => feed({ posts: [], total: 0 }),
      topics: () => ({ topics: [], proposed: [], unfiled: 0 }),
    });
    look({ topic: ROOT });
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(view.all("[data-node]").map((node) => node.dataset.node)).toEqual([
      ROOT,
      LEFT,
      RIGHT,
      SHARED,
    ]);
    expect(view.one(`[data-node="${SHARED}"]`).textContent).toContain("depth 2");
    const normalized = view.one('[data-relation="part-of"] p');
    expect(
      [...normalized.querySelectorAll("[data-entity]")].map((node) =>
        node.getAttribute("data-entity"),
      ),
    ).toEqual([ROOT, LEFT]);
    expect(view.all('[data-relation="contains"]')).toHaveLength(4);
    expect(view.all(`[data-node="${OUTSIDE}"]`)).toHaveLength(0);
    expect(view.one('[aria-label="Other relations"]').textContent).toContain("depends-on");
    expect(fake.to("neighborhood").map((call) => call.args)).toEqual([{ entityId: ROOT }]);
    expect(fake.last("feed")).toMatchObject({ topic: ROOT, sort: "new", limit: 15 });

    const button = view.one(`.babel-neighborhood-entities [data-entity="${LEFT}"]`);
    button.focus();
    expect(document.activeElement).toBe(button);
    await view.press(`.babel-neighborhood-entities [data-entity="${LEFT}"]`);
    await view.settle();
    expect(fake.opened.at(-1)).toEqual({
      panelId: `${FEED_PLUGIN_ID}.${PANELS.topic}`,
      arg: { topic: LEFT },
    });
    expect(view.one(".babel-topic-name").textContent).toBe("t/Empty service");
    expect(view.one(".babel-neighborhood").textContent).toContain("empty neighbourhood");
    expect(view.all("[data-node]").map((node) => node.dataset.node)).toEqual([LEFT]);
    expect(fake.to("neighborhood").map((call) => call.args)).toEqual([
      { entityId: ROOT },
      { entityId: LEFT },
    ]);
    expect(fake.last("feed")).toMatchObject({ topic: LEFT, sort: "new", limit: 15 });
    expect(
      fake.calls.every((call) =>
        ["topic", "neighborhood", "feed", "topics", "pulse"].some(
          (name) => call.name === `atyrode.babel.${name}`,
        ),
      ),
    ).toBe(true);
    await view.unmount();
  });

  test("uses the resolved entity id rather than an ID-shaped topic name", async () => {
    const shapedName = "ent_00000099";
    const actual = stored();
    const fake = fakeHost({
      topic: () => topic({ topic: { ...topic().topic!, id: ROOT, name: shapedName } }),
      neighborhood: (args) => {
        expect(NeighborhoodQuerySchema.parse(args).entityId).toBe(ROOT);
        return actual;
      },
      feed: () => feed({ posts: [], total: 0 }),
      topics: () => ({ topics: [], proposed: [], unfiled: 0 }),
    });
    const view = await mount(<TopicPanel host={fake.host} arg={{ topic: shapedName }} />);
    expect(view.one(".babel-topic-name").textContent).toBe(`t/${shapedName}`);
    expect(fake.to("neighborhood").map((call) => call.args)).toEqual([{ entityId: ROOT }]);
    await view.unmount();
  });

  test("keeps stored authority, dated stale/disputed history and current sources separate while opening records", async () => {
    const fake = fakeHost({ neighborhood: () => stored() });
    const view = await mount(<Neighborhood host={fake.host} entityId={ROOT} />);
    const fact = view.one('[data-fact="fact-disputed"]');
    expect(fact.textContent).toContain("Authority: operator p1");
    expect(fact.textContent).toContain("disputed");
    expect(fact.textContent).toContain("reviewer-1");
    expect(fact.textContent).toContain("2026-09-12T00:00:00Z");
    expect(fact.textContent).toContain("valid until 2026-09-11T00:00:00Z");
    expect(fact.textContent).toContain("fact-replacement");
    const filed = view.one('[data-filed-record="pro_0000000a"]');
    expect(filed.textContent).toContain("stale");
    expect(filed.textContent).toContain("recipe-service@3");
    expect(filed.textContent).toContain("Operator ruling: unknown");
    expect(view.one('[aria-label="Filed records"]').textContent).toContain("heuristic filing");
    expect(view.one('[aria-label="Questions and answers"]').textContent).toContain(
      "Use the enrolled machine.",
    );
    expect(
      view.one('[aria-label="Questions and answers"] [data-state="open"]').textContent,
    ).toContain("no status event recorded");
    expect(view.one('[aria-label="Contradictions"]').textContent).toContain("fact-disputed");
    await view.press('[aria-label="Contradictions"] [data-record="fnd_0000000b"]');
    expect(fake.opened.at(-1)).toEqual({
      panelId: `${FEED_PLUGIN_ID}.${PANELS.record}`,
      arg: { recordId: "fnd_0000000b" },
    });
    const sources = view.one('[aria-label="Current catalog sources"]');
    expect(sources.textContent).toContain("NOT historical citation proof");
    expect(sources.textContent).toContain("snapshot-today");
    expect(sources.textContent).toContain("review state: unknown");
    expect(view.text()).toContain("Archive access: unknown; not probed");
    await view.press('[data-filed-record="pro_0000000a"] .disclosure__header');
    expect(view.one(".babel-neighborhood-payload").textContent).toBe(
      stored().records[0]!.payloadJson,
    );
    expect(view.all("blockquote")).toHaveLength(0);
    await view.press('[data-filed-record="pro_0000000a"] [data-record]');
    expect(looking().recordId).toBe("pro_0000000a");
    expect(fake.opened.at(-1)).toEqual({
      panelId: `${FEED_PLUGIN_ID}.${PANELS.record}`,
      arg: { recordId: "pro_0000000a" },
    });
    expect(fake.calls.map((call) => call.name)).toEqual(["atyrode.babel.neighborhood"]);
    await view.unmount();
  });

  test("refreshes partial, changed, missing, empty and unavailable states without retaining the old facts as current", async () => {
    let response = stored();
    let denied = false;
    const fake = fakeHost({
      neighborhood: () => {
        if (denied) throw new Denial("read grant unavailable");
        return response;
      },
    });
    const view = await mount(<Neighborhood host={fake.host} entityId={ROOT} />);
    response = {
      ...response,
      facts: response.facts.map((fact) => ({
        ...fact,
        value: "online",
        status: { ...fact.status!, state: "stale" },
      })),
      coverage: {
        ...response.coverage,
        truncated: true,
        traversalComplete: false,
        recordsComplete: false,
        reasons: ["depth", "nodes", "items", "bytes", "unavailable"],
        omittedNodesAtLeast: 3,
        omittedItems: 7,
        unavailableEntities: 1,
      },
    };
    fake.announce();
    await view.wait(120);
    const coverage = view.one(".babel-neighborhood-coverage");
    expect(coverage.textContent).toContain("Partial neighbourhood");
    for (const reason of [
      "Depth limit",
      "Node limit",
      "Item limit",
      "Byte limit",
      "Entity material unavailable",
    ]) {
      expect(coverage.textContent).toContain(reason);
    }
    expect(coverage.textContent).toContain("not a complete project inventory");
    expect(coverage.textContent).toContain("at least 3 nodes omitted");
    expect(coverage.textContent).toContain("7 material rows omitted");
    expect(coverage.textContent).toContain("1 unavailable entities");
    expect(view.one('[data-fact="fact-disputed"]').textContent).toContain("online");
    expect(view.one('[data-fact="fact-disputed"] .babel-neighborhood-status').dataset.state).toBe(
      "stale",
    );

    denied = true;
    fake.announce();
    await view.wait(120);
    expect(view.text()).toContain("Neighbourhood unavailable: read grant unavailable");
    expect(view.all("[data-fact]")).toHaveLength(0);
    expect(view.text()).not.toContain("empty neighbourhood");
    denied = false;
    response = neighborhood({
      state: "missing",
      nodes: [],
      coverage: {
        ...neighborhood().coverage,
        traversalComplete: false,
        recordsComplete: false,
        visitedNodes: 0,
        unavailableEntities: 1,
        reasons: ["unavailable"],
      },
    });
    await view.press(".babel-neighborhood button");
    await view.settle();
    expect(view.one(".babel-neighborhood-missing").textContent).toContain(ROOT);
    expect(view.all("[data-node]")).toHaveLength(0);
    response = neighborhood();
    fake.announce();
    await view.wait(120);
    expect(view.text()).toContain("empty neighbourhood");
    expect(view.all(".babel-neighborhood-missing")).toHaveLength(0);
    expect(view.all("[data-fact]")).toHaveLength(0);
    expect(view.one(".babel-neighborhood-coverage").textContent).not.toContain(
      "Partial neighbourhood",
    );
    await view.unmount();
  });

  test("does not call a bounded empty response an empty neighbourhood when material was omitted", async () => {
    const response = neighborhood({
      coverage: {
        ...neighborhood().coverage,
        truncated: true,
        recordsComplete: false,
        reasons: ["bytes"],
        omittedItems: 1,
      },
    });
    const fake = fakeHost({ neighborhood: () => response });
    const view = await mount(<Neighborhood host={fake.host} entityId={ROOT} />);
    expect(view.text()).toContain("Partial neighbourhood");
    expect(view.text()).toContain("No facts returned in this scope");
    expect(view.text()).not.toContain("empty neighbourhood");
    await view.unmount();
  });

  test("reports a refused record seat while still pointing the existing peek pane at the record", async () => {
    const fake = fakeHost({ neighborhood: () => stored() }, () => ({
      ok: false,
      refused: "no_tile",
    }));
    const view = await mount(<Neighborhood host={fake.host} entityId={ROOT} />);
    await view.press('[data-filed-record="pro_0000000a"] [data-record]');
    expect(view.text()).toContain(NO_SEAT);
    expect(looking().recordId).toBe("pro_0000000a");
    await view.unmount();
  });
});
