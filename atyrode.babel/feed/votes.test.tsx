import { describe, expect, test } from "bun:test";
import { Votes } from "./votes.tsx";
import { mount, post } from "./testing.tsx";

/*
  THE VOTE STRIP is the one figure on a row the operator reads as a judgement, so the two
  distinctions §8.5 turns on are what is tested: an unreviewed record must not read as an
  unopposed one, and a role must be legible as the question it answers rather than as one more
  vote for the same thing.
*/

describe("the vote strip", () => {
  test("draws one dot per assessment, coloured by vote and shaped by role", async () => {
    const view = await mount(<Votes post={post()} ticked={false} />);
    const dots = view.all(".babel-dot");
    expect(dots.map((dot) => dot.getAttribute("data-role"))).toEqual(["reception", "evidence", "challenge"]);
    expect(dots.map((dot) => dot.getAttribute("data-tone"))).toEqual(["good", "good", "bad"]);
    // The shape says which question was answered; two supports on two roles are two marks.
    expect(dots.map((dot) => dot.textContent)).toEqual(["●", "■", "▲"]);
    expect(view.one(".babel-score").textContent).toBe("1");
    await view.unmount();
  });

  test("a record no reviewer assessed shows the ring and no figure at all", async () => {
    const view = await mount(
      <Votes post={post({ votes: [], support: 0, oppose: 0, unsure: 0, score: 0, contested: false })} ticked={false} />,
    );
    const dots = view.all(".babel-dot");
    expect(dots).toHaveLength(1);
    expect(dots[0]?.getAttribute("data-tone")).toBe("none");
    expect(dots[0]?.getAttribute("title")).toBe("not yet reviewed");
    // A nought over nothing reads as "nobody objected", which is the defect §8.5 names.
    expect(view.all(".babel-score")).toHaveLength(0);
    await view.unmount();
  });

  test("a split reception is marked before the figure it qualifies", async () => {
    const view = await mount(<Votes post={post()} ticked={false} />);
    const line = view.one(".babel-score-line");
    expect(line.firstElementChild?.className).toContain("babel-contested");
    await view.unmount();
  });

  test("totals without assessments still colour, and never invent a role shape", async () => {
    const view = await mount(
      <Votes post={post({ votes: [], support: 1, oppose: 0, unsure: 1, score: 1 })} ticked={false} />,
    );
    const dots = view.all(".babel-dot");
    expect(dots.map((dot) => dot.getAttribute("data-tone"))).toEqual(["good", "faint"]);
    expect(dots.every((dot) => dot.getAttribute("data-role") === null)).toBe(true);
    expect(dots.every((dot) => dot.textContent === "●")).toBe(true);
    await view.unmount();
  });

  test("past five marks the strip counts the rest instead of drawing a chart", async () => {
    const many = Array.from({ length: 8 }, () => ({ role: "reception" as const, vote: "support" as const }));
    const view = await mount(<Votes post={post({ votes: many })} ticked={false} />);
    expect(view.all(".babel-dot")).toHaveLength(5);
    expect(view.one(".babel-dots-more").textContent).toBe("+3");
    await view.unmount();
  });

  test("a zero score is dimmed rather than absent when reviewers did assess it", async () => {
    const view = await mount(
      <Votes post={post({ score: 0, votes: [{ role: "reception", vote: "unsure" }] })} ticked={false} />,
    );
    expect(view.one(".babel-score").hasAttribute("data-zero")).toBe(true);
    await view.unmount();
  });
});
