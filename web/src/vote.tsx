import { useEffect, useId, useState, type ReactElement } from "react";
import { errorMessage } from "./format";
import { putReception, type OperatorStance } from "./recordapi";
// The arrows' own rules live in the record's stylesheet rather than in a third
// one, because they are the record's act: §8.6 puts the operator's voice on the
// record and §8.7 puts the same control on every row of the feed. Importing it
// here is what makes the dependency true wherever the arrows are mounted,
// rather than true only because the record page happens to be in the bundle.
import "./record.css";

// The score and the two arrows, wherever a score appears.
//
// §8.7: "a score is a count of votes, and every vote is attributed". Babel's
// reviewers vote through §4.12's assessments and the operator votes with
// §8.6's stance — agree, disagree, unsure — and the arrows are that stance.
// One component for the feed row and the record's post header, because two
// would be two ways to record one thing: the optimistic update, the withdrawal
// gesture and the breakdown are the same act in both places, and the second
// implementation is the one that drifts.
//
// Three rules it keeps, each one a section of the specification:
//
//   - A lit arrow pressed again records `unsure`. §8.7 calls unsure "the
//     honest name for a withdrawn vote", so the control has no third button
//     and no way to leave a vote the operator has taken back.
//   - The breakdown is one gesture away and separates the two voices. §4.12's
//     boundary is that a person never mints what reads as a model's
//     observation, so the title says "you: agree · Babel: 5 support…" and
//     never one merged sentence.
//   - No votes is not a score of nought. §8.5 refuses evaluation data rendered
//     as zero opposition, so an unvoted record shows an em dash and says so.

// VoteTally is the deployment's whole count on one record: Babel's reviewers
// and the operator together, which is what §8.7's one number is. `you` is the
// operator's own current stance, kept separate so the breakdown can attribute
// his vote and subtract it back out of Babel's.
export interface VoteTally {
  score: number;
  support: number;
  oppose: number;
  unsure: number;
  you: string;
}

// What the operator says now, with the empty string for "nothing". It is not
// `undefined`: the feed sends a field for every post and an absent stance is a
// value on the wire rather than a missing key.
export type VoteStance = OperatorStance | "";

// The two arrows, as the stances they record. Up is agree and down is
// disagree; unsure has no arrow of its own because it is what pressing a lit
// one records.
type Arrow = "agree" | "disagree";

export function VoteArrows({
  id,
  score,
  support,
  oppose,
  unsure,
  you,
  onVoted,
}: {
  id: string;
  score: number;
  support: number;
  oppose: number;
  unsure: number;
  you: VoteStance;
  onVoted?: (next: VoteTally) => void;
}): ReactElement {
  // The tally the arrows are showing, which is the props until the operator
  // acts and the post's own count again as soon as a read carries it. A stance
  // is attributed, reversible and decides nothing, so it posts immediately and
  // optimistically and there is nothing to confirm.
  const [tally, setTally] = useState<VoteTally>({ score, support, oppose, unsure, you });
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const described = useId();

  useEffect(() => {
    setTally({ score, support, oppose, unsure, you });
  }, [score, support, oppose, unsure, you]);

  async function press(arrow: Arrow) {
    const previous = tally;
    // Pressing the arrow already lit is the withdrawal, and `unsure` is what
    // the store records for it: §4.12 appends, so a taken-back vote is a
    // later vote with no side rather than a deleted one.
    const stance: OperatorStance = tally.you === arrow ? "unsure" : arrow;
    const next = withStance(tally, stance);
    setTally(next);
    setPending(true);
    setFailure(null);
    try {
      await putReception(id, stance);
      onVoted?.(next);
    } catch (error) {
      // A refused vote leaves the number it was showing before. The arrows
      // are the only thing on a feed row that writes, and a row that kept an
      // optimistic count after the write failed would be a score nobody
      // recorded.
      setTally(previous);
      setFailure(errorMessage(error));
    } finally {
      setPending(false);
    }
  }

  const breakdown = breakdownOf(tally);
  const voted = tally.support + tally.oppose + tally.unsure > 0;

  return (
    <div className="vote" role="group" aria-label="Your vote" aria-describedby={described}>
      <button
        type="button"
        className="vote-up"
        data-stance="agree"
        aria-pressed={tally.you === "agree"}
        aria-label={tally.you === "agree" ? "Withdraw your agreement" : "Agree"}
        title={breakdown}
        disabled={pending}
        onClick={() => press("agree")}
      >
        ▲
      </button>
      {/* The number, in the observatory's face because it is a figure. It is
          focusable rather than inert: the breakdown is one gesture away for a
          pointer through the title and for a keyboard through the description,
          and a score with no way to ask what it is made of is exactly the
          merged number §4.12 refuses. */}
      <span
        className="vote-score"
        title={breakdown}
        tabIndex={0}
        aria-describedby={described}
      >
        {voted ? tally.score : "—"}
      </span>
      <button
        type="button"
        className="vote-down"
        data-stance="disagree"
        aria-pressed={tally.you === "disagree"}
        aria-label={tally.you === "disagree" ? "Withdraw your disagreement" : "Disagree"}
        title={breakdown}
        disabled={pending}
        onClick={() => press("disagree")}
      >
        ▼
      </button>
      <span className="sr-only" id={described}>
        {breakdown}
      </span>
      {failure && (
        <span className="vote-error inline-error" role="alert">
          {failure}
        </span>
      )}
    </div>
  );
}

// withStance is the tally the operator's next vote produces.
//
// One voter changing his mind is one vote moving between columns, so his
// current side is taken out before the new one is added: agreeing after
// disagreeing is not two votes. The score is derived rather than adjusted —
// §8.7 defines it as support minus oppose and a second arithmetic for the
// optimistic case is how the number on screen comes to disagree with the
// number the next read carries.
function withStance(tally: VoteTally, stance: OperatorStance): VoteTally {
  const support = tally.support - (tally.you === "agree" ? 1 : 0) + (stance === "agree" ? 1 : 0);
  const oppose =
    tally.oppose - (tally.you === "disagree" ? 1 : 0) + (stance === "disagree" ? 1 : 0);
  const unsure = tally.unsure - (tally.you === "unsure" ? 1 : 0) + (stance === "unsure" ? 1 : 0);
  return { score: support - oppose, support, oppose, unsure, you: stance };
}

// breakdownOf says what the one number is made of, in one line, with the two
// voices apart.
//
// Babel's own numbers are the totals less the operator's single vote, which is
// the only way one field can carry the score §8.7 asks for and still answer
// "what did Babel say" without a second request. A deployment with no
// reviewer votes says so: "0 support, 0 oppose" would read as a unanimous
// absence of opposition, and §8.5 refuses exactly that rendering.
function breakdownOf(tally: VoteTally): string {
  const mine = tally.you === "" ? "you: no vote" : `you: ${tally.you}`;
  const support = tally.support - (tally.you === "agree" ? 1 : 0);
  const oppose = tally.oppose - (tally.you === "disagree" ? 1 : 0);
  const unsure = tally.unsure - (tally.you === "unsure" ? 1 : 0);
  if (support + oppose + unsure === 0) return `${mine} · Babel: no votes yet`;
  return `${mine} · Babel: ${support} support, ${oppose} oppose, ${unsure} unsure`;
}
