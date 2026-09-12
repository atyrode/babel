import { useSearchParams } from "react-router-dom";
import ArchiveSection from "./ArchivePage";
import CeilingsSection from "./FocusPage";
import HelpSection from "./HelpPage";
import PolicySection from "./EvaluationPolicyPage";
import SpendCeilingsSection from "./SpendCeilingsPage";
import "../settings.css";

// Settings holds what the operator configures and then stops thinking about.
//
// Four surfaces had primary nav entries or near-entries of their own and none
// of them is something a person opens Babel to read: a repository of restic
// snapshots, what evaluation work may cost, what analysis may spend on one
// subject, and the orientation text. They are sections here rather than four
// more destinations, because the cost of a nav entry is paid by every reader
// on every page and the benefit is paid to whoever visits once a month.
//
// The section is query state rather than a route for one reason: a Settings
// section is not a place, it is a drawer. Writing it into the URL still lets
// an operator link to one — and the redirects from /archive, /help,
// /evaluation/policy and /reality/focus depend on that.

const SECTIONS = [
  {
    value: "archive",
    label: "Archive",
    title: "Inspect repository coverage and verify archived data.",
  },
  {
    value: "ceilings",
    label: "Ceilings",
    title:
      "What Babel is allowed to spend: on autonomous work at all, and on each subject. A policy " +
      "you state here is your own fact in the ledger, in force until you supersede it: nothing " +
      "is deleted, and a subject's sessions are never removed from the corpus.",
  },
  {
    value: "policy",
    label: "Review policy",
    title:
      "What authorized evaluation work is allowed to cost and how it chooses what to read. " +
      "Saving these settings starts nothing.",
  },
  {
    value: "help",
    label: "What Babel is",
    title:
      "Babel is an exploratory instrument for archived agent conversations. It records where " +
      "an idea came from and how it was investigated; it does not promise the idea is correct.",
  },
];

function SettingsPage() {
  const [params, setParams] = useSearchParams();
  const asked = params.get("section") ?? "";
  const section = SECTIONS.some((entry) => entry.value === asked) ? asked : "archive";

  // Changing section drops the other section's query state. `subject` belongs
  // to the ceilings picker and would otherwise follow the reader into the
  // archive, where it means nothing and would be written back into a link he
  // shares.
  function show(value: string) {
    const next = new URLSearchParams();
    next.set("section", value);
    setParams(next);
  }

  return (
    <section className="page settings-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Configuration</p>
          <h1>Settings</h1>
        </div>
      </div>

      <nav className="section-nav" aria-label="Settings sections">
        {SECTIONS.map((entry) => (
          <button
            type="button"
            key={entry.value}
            className={section === entry.value ? "active" : undefined}
            aria-pressed={section === entry.value}
            title={entry.title}
            onClick={() => show(entry.value)}
          >
            {entry.label}
          </button>
        ))}
      </nav>

      {section === "archive" && <ArchiveSection />}
      {section === "ceilings" && (
        <>
          {/* Two ceilings under one word, and they were not the same thing.
              The spend ceilings are what the machine may spend on autonomous
              work at all — the two numbers `babel conductor configure` holds,
              and the ones Watch's own refusal sends the operator here to set.
              The focus policy below is per subject: what analysis may spend on
              this project rather than that one. Both belong in this section;
              which one a refusal meant has to be legible, so each carries its
              own heading and the one the refusal names is first. */}
          <SpendCeilingsSection />
          <div className="section-heading settings-subheading">
            <div>
              <p className="eyebrow">Per subject</p>
              <h2>Focus policy</h2>
            </div>
          </div>
          <CeilingsSection />
        </>
      )}
      {section === "policy" && <PolicySection />}
      {section === "help" && <HelpSection />}
    </section>
  );
}

export default SettingsPage;
