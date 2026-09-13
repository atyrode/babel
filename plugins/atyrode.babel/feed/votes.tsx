import type { ReactElement } from "react";
import { ROLES } from "../contract.ts";
import type { FeedPost, FeedVote } from "./api.ts";

/*
  WHAT BABEL'S REVIEWERS SAID, as the row's left column (SPEC §8.5, §4.12).

  The figure is the score and the marks under it are what it is made of: one per assessment,
  COLOURED BY THE VOTE and SHAPED BY THE ROLE. A role is the question a run was asked, so four
  supports across four roles are four answers to four different questions — same colour,
  different mark, and the row says how broadly it was assessed without carrying a table.

  A record no reviewer has assessed wears a hollow ring and NO figure. That is the distinction
  §8.5 turns on: a nought over nothing reads as a record nobody objected to.
*/

/** One mark per role. A role this build has no shape for is a plain dot: the vote is real. */
const ROLE_SHAPES: Record<string, string> = {
  reception: "●",
  evidence: "■",
  challenge: "▲",
  relevance: "◆",
  comparison: "▬",
  outcome: "★",
  filing: "◇",
  backlog: "◇",
};

/** What a vote's colour says, in the palette's own three signals. */
const VOTE_TONES: Record<string, string> = {
  support: "good",
  oppose: "bad",
  unsure: "faint",
};

/** How many marks a row draws before it counts them instead: five read as a set, eleven as a chart. */
const VOTES_SHOWN = 5;

const ROLE_ORDER: readonly string[] = ROLES;

/**
 * The assessments the feed carried, or the totals when it carried none: same colours, no role
 * shape, because a shape this page invented would be a claim about which question was answered.
 */
function marks(post: FeedPost): FeedVote[] {
  if (post.votes.length > 0) {
    return [...post.votes].sort(
      (left, right) => ROLE_ORDER.indexOf(left.role) - ROLE_ORDER.indexOf(right.role),
    );
  }
  return [
    ...Array.from({ length: post.support }, (): FeedVote => ({ role: "", vote: "support" })),
    ...Array.from({ length: post.oppose }, (): FeedVote => ({ role: "", vote: "oppose" })),
    ...Array.from({ length: post.unsure }, (): FeedVote => ({ role: "", vote: "unsure" })),
  ];
}

export function Votes({ post, ticked }: { post: FeedPost; ticked: boolean }): ReactElement {
  const votes = marks(post);
  const shown = votes.slice(0, VOTES_SHOWN);
  const rest = votes.length - shown.length;
  const breakdown = `Babel's reviewers: ${post.support} support, ${post.oppose} oppose, ${post.unsure} unsure`;
  return (
    <div className="babel-votes">
      <span className="babel-score-line">
        {/* Reviewers on both sides of one claim, which a single figure cannot say. It leads
            the number because it qualifies it. */}
        {post.contested && (
          <span
            className="babel-contested"
            title="Babel's reviewers are split on this"
            aria-label="Babel's reviewers are split on this"
          />
        )}
        {votes.length > 0 && (
          <span
            className="babel-score"
            data-zero={post.score === 0 ? "" : undefined}
            data-ticked={ticked ? "" : undefined}
            title={breakdown}
            aria-label={breakdown}
          >
            {post.score}
          </span>
        )}
      </span>
      <span className="babel-dots">
        {votes.length === 0 ? (
          <span
            className="babel-dot"
            data-tone="none"
            title="not yet reviewed"
            aria-label="not yet reviewed"
          >
            ◯
          </span>
        ) : (
          <>
            {shown.map((vote, index) => (
              <span
                className="babel-dot"
                key={`${vote.role}-${vote.vote}-${String(index)}`}
                data-tone={VOTE_TONES[vote.vote] ?? "faint"}
                data-role={vote.role === "" ? undefined : vote.role}
                title={vote.role === "" ? vote.vote : `${vote.role}: ${vote.vote}`}
              >
                {ROLE_SHAPES[vote.role] ?? "●"}
              </span>
            ))}
            {rest > 0 && <span className="babel-dots-more">+{rest}</span>}
          </>
        )}
      </span>
    </div>
  );
}
