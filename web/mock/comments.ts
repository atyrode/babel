// The conversation under a post: GET and POST /api/record/{id}/comments.
//
// §8.7 makes five kinds of record one thread — a reviewer's contribution
// prose, a refinement, the operator's reason in his own words, the answer to a
// question and the reason on a reconsideration — with the rulings beside them
// as the attributed acts they are. The fixtures here are that thread for one
// record, because the shapes the surface has to render well are the ones a
// browser has to be able to reach: a reviewer's prose with the role it was
// asked under, an answer nested beneath what it answers, the operator's own
// words, and two rulings sitting in chronological place among them.
//
// Every other record answers with an empty thread, which is the other state
// the page must render honestly: "No comments yet" above the box, never a
// frame around nothing.
//
// This file is its own rather than part of ./record.ts because the peel and
// the thread are two reads with two bodies of fixture state, and #234's whole
// diagnosis is what happens when one file holds everything a surface touches.

// A reviewer's prose is a model's output, so one line of it carries ./phaseb.ts's
// hostile markup. The thread is a surface that did not exist when that fixture
// was written, and prose written by a model is exactly where §2.7's inertness
// has to hold: the markup is imported rather than copied so the two cannot
// drift into being different attacks.
import { HOSTILE_HTML } from "./phaseb";

// The record whose conversation the preview holds: the consolidated proposal,
// which is the first proposal ./phaseb.ts describes and the one the record
// page is normally opened on.
const THREAD_SUBJECT = "pro_criteria-template";

// The operator, as the server resolves him. The client never sends an author
// — §4.12 resolves the launch session's identity server-side and the write
// route refuses an author field — so this is the mock standing in for that
// resolution, and it is the same name the ruling fixtures are attributed to.
const OPERATOR = "alex";

interface MockAuthor {
  kind: "run" | "operator";
  id: string;
  href: string;
}

interface MockComment {
  id: string;
  kind: "contribution" | "refinement" | "reason" | "answer" | "reconsideration";
  author: MockAuthor;
  role: string;
  text: string;
  at: string;
  related_id: string;
  replies?: MockComment[];
}

interface MockAct {
  id: string;
  act: "accept" | "reject" | "defer" | "duplicate" | "reopen";
  by: string;
  at: string;
  reason: string;
}

function run(id: string): MockAuthor {
  return { kind: "run", id, href: `/watch/runs/${id}` };
}

// The operator has no page, so his href is empty rather than a route that
// would resolve to nothing.
const operator: MockAuthor = { kind: "operator", id: OPERATOR, href: "" };

// Seven lines and one answer, in the order they were written. The renderer
// orders the thread newest-first; a fixture written newest-first would hide a
// sorting bug rather than expose one.
//
// The prose is synthetic and says what a real reviewer's contribution says:
// what it checked, what it found, and what it did not settle. One line carries
// escaped newlines — `\u{A}` is what the sanitizer leaves for a line break —
// because a reviewer's paragraph is the one comment shape that has to survive
// the trip through it.
const fixture: MockComment[] = [
  {
    id: "cmt_evidence-check",
    kind: "contribution",
    author: run("run_challenge-08"),
    role: "evidence",
    text:
      "Both citations resolve and the excerpts match their digests.\\u{A}\\u{A}The supporting " +
      "session does close with criteria stated; the conflicting one closes without them. That " +
      "is two sessions, not a pattern, and the proposal's own uncertainty says so.",
    at: "2026-08-29T08:10:00Z",
    related_id: "",
  },
  {
    id: "cmt_reception-1",
    kind: "contribution",
    author: run("run_challenge-08"),
    role: "reception",
    text:
      "Worth trialling. The remedy is one template file and it is reversible, which is the " +
      "cheapest possible test of the pattern.",
    at: "2026-08-29T08:14:00Z",
    related_id: "",
  },
  {
    id: "cmt_operator-not-yet",
    kind: "reason",
    author: operator,
    role: "",
    text:
      "Not until the corpus is real. Two synthetic sessions is not a reason to change how every " +
      "session starts.",
    at: "2026-08-30T09:20:00Z",
    related_id: "",
    replies: [
      {
        id: "cmt_answered-scope",
        kind: "answer",
        author: run("run_challenge-08"),
        role: "",
        text:
          "Scoped it to the two harnesses that recorded the closes, so the trial does not touch " +
          "templates the pattern was never observed in.",
        at: "2026-08-30T11:02:00Z",
        related_id: "cmt_operator-not-yet",
      },
    ],
  },
  {
    id: "cmt_refinement-1",
    kind: "refinement",
    author: run("run_challenge-08"),
    role: "",
    text:
      "Reworded the outcome to say the template change is reviewable rather than applied, after " +
      "the objection that Babel was proposing to edit the operator's dotfiles.",
    at: "2026-08-30T11:05:00Z",
    related_id: "",
  },
  {
    id: "cmt_reception-2",
    kind: "contribution",
    author: run("run_discovery-07"),
    role: "reception",
    text: "No position. The claim is about verification and this reviewer only saw the closes.",
    at: "2026-08-31T06:40:00Z",
    related_id: "",
  },
  {
    id: "cmt_comparison",
    kind: "contribution",
    author: run("run_discovery-07"),
    role: "comparison",
    text:
      "The other remedy for the same pain — skipping describes on unchanged digests — does not " +
      "address closures at all. They are not alternatives.",
    at: "2026-09-01T07:15:00Z",
    related_id: "",
  },
  {
    id: "cmt_hostile-contribution",
    kind: "contribution",
    author: run("run_discovery-07"),
    role: "evidence",
    text: "The excerpt this claim rests on reads: " + HOSTILE_HTML,
    at: "2026-09-01T07:20:00Z",
    related_id: "",
  },
  {
    id: "cmt_reconsidered",
    kind: "reconsideration",
    author: operator,
    role: "",
    text:
      "Reopening it: the same pattern turned up twice more this week, which is the change I said " +
      "I was waiting for.",
    at: "2026-09-08T10:30:00Z",
    related_id: "",
  },
];

// Two rulings, in the thread where they happened. A deferral and the reopen
// that lifted it: §4.7 appends rather than edits, so both are here, and the
// record is undecided again rather than deferred — which is exactly what the
// peel's standing says about it.
const acts: MockAct[] = [
  {
    id: "rul_deferred",
    act: "defer",
    by: OPERATOR,
    at: "2026-08-30T09:22:00Z",
    reason: "Waiting for the pattern on a real corpus.",
  },
  {
    id: "rul_reopened",
    act: "reopen",
    by: OPERATOR,
    at: "2026-09-08T10:31:00Z",
    reason: "Two further occurrences since the deferral.",
  },
];

// What the operator has written in this preview, newest last. It is mutated by
// a POST so the box is exercisable end to end in one browser: write a comment,
// find it at the top of the thread, reload the page and find it still there.
const posted: Record<string, MockComment[]> = {};
let written = 0;

// counted totals the conversation, replies included. Acts are not counted: the
// heading says how much conversation there is, and a ruling is not part of it.
function counted(comments: MockComment[]): number {
  let total = 0;
  for (const comment of comments) {
    total += 1 + counted(comment.replies ?? []);
  }
  return total;
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

export async function commentsResponse(request: Request, url: URL): Promise<Response | null> {
  const path = url.pathname;
  if (!path.startsWith("/api/record/") || !path.endsWith("/comments")) return null;
  const id = decodeURIComponent(path.slice("/api/record/".length, -"/comments".length));
  // An id with a separator in it is some other sub-resource's path, and this
  // route claims exactly one shape.
  if (!id || id.includes("/")) return null;

  if (request.method === "POST") {
    const body = (await request.json().catch(() => ({}))) as { text?: unknown };
    const text = typeof body.text === "string" ? body.text : "";
    if (!text.trim()) {
      // The real route refuses an empty comment rather than storing a reason
      // the operator did not give.
      return json({ error: "a comment needs text" }, 400);
    }
    written += 1;
    const comment: MockComment = {
      id: `cmt_own-${written}`,
      // His own words on a record are a reason, and posting one is a feedback
      // record with no polarity: §8.7 keeps the vote in the arrows, so nothing
      // about this write touches the score.
      kind: "reason",
      author: operator,
      role: "",
      // The real server escapes the operator's bytes before storing them and
      // the surface renders the escaped form as text; the mock keeps what was
      // sent, which is the same string for everything a keyboard produces.
      text,
      at: new Date().toISOString(),
      related_id: "",
    };
    posted[id] = [...(posted[id] ?? []), comment];
    return json({ comment }, 201);
  }

  if (request.method !== "GET") return json({ error: "method not allowed" }, 405);

  const own = posted[id] ?? [];
  const comments = id === THREAD_SUBJECT ? [...fixture, ...own] : own;
  return json({
    comments,
    acts: id === THREAD_SUBJECT ? acts : [],
    total: counted(comments),
  });
}
