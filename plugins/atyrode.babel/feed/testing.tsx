import { GlobalRegistrator } from "@happy-dom/global-registrator";

/*
  THE DOM these panels are rendered into, registered ONCE for the process. It lives here
  rather than in each test file because `bun test` runs the three of them in one process and
  a second registration throws; importing this module is what a test file does to get a DOM,
  a fake host and the fixtures, and the three arrive together.
*/
if (!("happyDOM" in globalThis)) {
  GlobalRegistrator.register();
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
}

import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { HostServices } from "@manifold/plugin";
import type { ActionOutcome } from "@manifold/protocol";
import type { FeedPost, FeedResult, RecordPeel, ThreadResult, TopicResult, TopicsResult } from "./api.ts";

/*
  THE TEST HOST AND THE FIXTURES, in one module because all three test files need the same
  ones and a fake host copied twice is a fake host that disagrees with itself.

  `fakeHost` answers doors from a table and RECORDS every call, which is what the assertions
  are about: a panel is judged by what it asked the hub and what it drew, never by its state.
  Everything the panels do not touch is absent from it rather than stubbed with a lie — a
  member a component starts using fails loudly here instead of silently answering undefined.
*/

export interface DoorCall {
  readonly name: string;
  readonly args: unknown;
}

export interface Fake {
  readonly host: HostServices;
  readonly calls: DoorCall[];
  /** The args of the last call to one door, for an assertion about what was posted. */
  last(name: string): unknown;
  /** Every call to one door, for an assertion about how many reads a gesture cost. */
  to(name: string): DoorCall[];
  /**
   * Fires the baseline's node event every polled reader subscribed to. It is how a test says
   * "the world changed": the feed's timer is off while the channel is up, so a live read
   * happens because an event arrived and for no other reason.
   */
  announce(): void;
}

/** A denial an answer may throw to make the door refuse instead of answering. */
export class Denial extends Error {}

export function fakeHost(answers: Record<string, (args: unknown) => unknown>): Fake {
  const listeners = new Set<() => void>();
  const calls: DoorCall[] = [];
  const client = {
    action: (name: string, args: unknown): Promise<ActionOutcome> => {
      calls.push({ name, args });
      const local = name.replace("atyrode.babel.", "");
      const answer = answers[local];
      if (answer === undefined) {
        return Promise.resolve({ ok: false, denial: { rule: "unknown_action", message: `no fake for ${local}` } });
      }
      try {
        return Promise.resolve({ ok: true, result: answer(args) });
      } catch (reason) {
        if (reason instanceof Denial) {
          return Promise.resolve({ ok: false, denial: { rule: "refused", message: reason.message } });
        }
        throw reason;
      }
    },
    subscribe: (_topics: unknown, handler: () => void) => {
      listeners.add(handler);
      return () => listeners.delete(handler);
    },
    status: "open" as const,
    on: () => () => undefined,
  };
  const host = {
    client,
    principal: { id: "p1", kind: "human", name: "the operator", color: "#4c8dff" },
    containerId: null,
    navigate: () => undefined,
    requestedRef: null,
  } as unknown as HostServices;
  return {
    host,
    calls,
    last: (name) => [...calls].reverse().find((call) => call.name === `atyrode.babel.${name}`)?.args,
    to: (name) => calls.filter((call) => call.name === `atyrode.babel.${name}`),
    announce: () => {
      for (const listener of listeners) listener();
    },
  };
}

// ---------------------------------------------------------------------------- rendering

export interface Mounted {
  readonly container: HTMLElement;
  readonly root: Root;
  /** Every element matching a selector, as the DOM holds them. */
  all(selector: string): HTMLElement[];
  one(selector: string): HTMLElement;
  text(): string;
  press(selector: string): Promise<void>;
  type(selector: string, value: string): Promise<void>;
  key(key: string): Promise<void>;
  settle(): Promise<void>;
  /** Lets a real timer fire — the halo, the fold and the toast are all wall-clock. */
  wait(ms: number): Promise<void>;
  unmount(): Promise<void>;
}

export async function mount(node: ReactElement): Promise<Mounted> {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => root.render(node));
  const one = (selector: string): HTMLElement => {
    const found = container.querySelector<HTMLElement>(selector);
    if (found === null) throw new Error(`nothing matches ${selector} in: ${container.textContent ?? ""}`);
    return found;
  };
  return {
    container,
    root,
    one,
    all: (selector) => [...container.querySelectorAll<HTMLElement>(selector)],
    text: () => container.textContent ?? "",
    press: async (selector) => {
      const element = one(selector);
      await act(async () => {
        element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    },
    type: async (selector, value) => {
      const field = one(selector);
      await act(async () => {
        Reflect.set(field, "value", value);
        field.dispatchEvent(new Event("input", { bubbles: true }));
      });
    },
    key: async (key) => {
      /*
        A real key press targets the FOCUSED element and bubbles from there through the
        document to the window, and the panels listen at both ends — the list on the window,
        an open menu on the document. Dispatching on the window alone would let a menu miss
        the Escape that closes it, which is a test passing about a surface that would not.
      */
      const target = document.activeElement ?? document.body;
      await act(async () => {
        target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
      });
    },
    settle: async () => {
      await act(async () => {
        await Promise.resolve();
      });
    },
    wait: async (ms) => {
      await act(async () => {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, ms);
        await promise;
      });
    },
    unmount: async () => {
      await act(async () => root.unmount());
      container.remove();
    },
  };
}

// ---------------------------------------------------------------------------- fixtures

export function post(overrides: Partial<FeedPost> = {}): FeedPost {
  return {
    id: "pro_0000000a",
    kind: "proposal",
    title: "Pin the engine profile before a run starts",
    standing: "new",
    createdAt: "2026-09-10T09:00:00Z",
    author: { runId: "run-20260910T165022Z" },
    topics: [{ id: "ent_0000beef", name: "babel" }],
    score: 1,
    support: 2,
    oppose: 1,
    unsure: 0,
    votes: [
      { role: "reception", vote: "support" },
      { role: "evidence", vote: "support" },
      { role: "challenge", vote: "oppose" },
    ],
    contested: true,
    reviewing: false,
    comments: 2,
    awaiting: true,
    why: "never ruled on · waiting 3d",
    lastActivityAt: "2026-09-11T09:00:00Z",
    ...overrides,
  };
}

export function feed(overrides: Partial<FeedResult> = {}): FeedResult {
  return {
    posts: [
      post(),
      post({
        id: "fnd_0000000b",
        kind: "finding",
        title: "Restic snapshots from dev-01 are hourly and complete",
        score: 0,
        support: 0,
        oppose: 0,
        unsure: 0,
        votes: [],
        contested: false,
        comments: 0,
        awaiting: false,
        why: "",
        topics: [],
      }),
      post({
        id: "que_0000000c",
        kind: "question",
        title: "Which repository is the shared catalog's source of truth?",
        score: 0,
        support: 0,
        oppose: 0,
        unsure: 0,
        votes: [{ role: "relevance", vote: "unsure" }],
        contested: false,
        reviewing: true,
        comments: 0,
        awaiting: true,
        why: "blocks a run · asked 1d",
      }),
    ],
    total: 3,
    builtAt: "2026-09-12T09:00:00Z",
    notice: "",
    ...overrides,
  };
}

export function topics(overrides: Partial<TopicsResult> = {}): TopicsResult {
  return {
    topics: [
      {
        id: "ent_0000beef",
        name: "babel",
        kind: "repository",
        binding: { kind: "repository", identity: "github.com/atyrode/babel", remote: "github.com/atyrode/babel", paths: ["/home/alex/babel"] },
        posts: 42,
        awaiting: 3,
        latestAt: "2026-09-12T08:00:00Z",
        interest: { state: "working", reason: "the rewrite", at: "2026-09-12T07:00:00Z", by: "the operator" },
      },
      {
        id: "ent_0000cafe",
        name: "manifold",
        kind: "repository",
        binding: null,
        posts: 7,
        awaiting: 0,
        latestAt: "2026-09-11T08:00:00Z",
        interest: { state: "", reason: "", at: "", by: "" },
      },
    ],
    proposed: [],
    unfiled: 12,
    ...overrides,
  };
}

export function topic(overrides: Partial<TopicResult> = {}): TopicResult {
  const rows = topics().topics;
  const first = rows[0];
  if (first === undefined) throw new Error("the topics fixture is empty");
  return { topic: first, proposed: [], feed: feed({ posts: [post()], total: 1 }), ...overrides };
}

export function peel(overrides: Partial<RecordPeel> = {}): RecordPeel {
  return {
    post: post(),
    claim: {
      statement: "A run should state its profile, model and ceiling before the first byte.",
      standing: "new",
      act: "Nobody has ruled on this.",
      ...overrides.claim,
    },
    case: {
      problem: "A run that names its model after the fact cannot be priced before it starts.",
      verification_criteria: ["The runtime report arrives before the first byte.", "The ceiling is refused, not clamped."],
    },
    evidence: [
      {
        excerpt: "I want to know what it will cost before I press go.",
        speaker: "the operator",
        session: { selector: "omp/2026-09-01T10:00:00Z", title: "planning the watch page", href: "/s/omp-1" },
        note: "Read as a requirement on the launch form.",
        line: 412,
      },
    ],
    reception: {
      byRole: [
        { role: "reception", support: 1, oppose: 0, unsure: 0, opposingRationales: [] },
        { role: "challenge", support: 0, oppose: 1, unsure: 0, opposingRationales: ["The report is not available on every machine."] },
      ],
      contested: true,
      operatorHistory: [],
    },
    machinery: { record: "pro_0000000a", revision: "3", policy: "2026-09-01" },
    related: [{ relation: "addresses", id: "hyp_0000000d", kind: "hypothesis", title: "Cost is invisible until a run ends" }],
    plan: null,
    ...overrides,
  };
}

export function thread(overrides: Partial<ThreadResult> = {}): ThreadResult {
  return {
    comments: [
      {
        id: "cmt_1",
        kind: "contribution",
        author: { kind: "run", id: "run-20260910T165022Z" },
        role: "challenge",
        text: "The runtime report is not available on a machine without the engine installed.",
        at: "2026-09-11T10:00:00Z",
        relatedId: "pro_0000000a",
        replies: [
          {
            id: "cmt_2",
            kind: "comment",
            author: { kind: "operator", id: "p1" },
            role: "",
            text: "Then the launch form says so instead of guessing.",
            at: "2026-09-11T11:00:00Z",
            relatedId: "cmt_1",
            replies: [],
          },
        ],
      },
    ],
    acts: [
      { id: "act_1", act: "defer", by: "the operator", at: "2026-09-11T12:00:00Z", reason: "waiting on the engine report" },
    ],
    total: 3,
    ...overrides,
  };
}
