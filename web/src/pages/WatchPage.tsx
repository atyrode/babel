import { useSearchParams } from "react-router-dom";
import ExploreSection from "./ExplorePage";
import FleetSection from "./FleetPage";

// Watch answers one question: what is it doing, and what did it cost?
//
// It is a re-presentation rather than a rewrite. Explore held this machine's
// receipts, the recipes that produced them and the retrieval they ran over;
// Fleet held what every machine says it is running. They were two nav entries
// for one question asked at two scopes, which is a distinction Babel makes
// internally and the reader does not: "is anything happening" does not become
// a different question because the answer is on another host.
//
// The scope is therefore a control on one page, written into the URL so a
// reader can link to the fleet view rather than describe how to reach it.

const SCOPES = [
  {
    value: "runs",
    label: "This machine",
    title: "Recipes, run receipts, and provenance-preserving corpus retrieval.",
  },
  {
    value: "fleet",
    label: "Every machine",
    title:
      "What each host says it is running, read from the shared catalog. Nothing here " +
      "starts, stops or steers a run on another machine: a row is a claim a process made " +
      "at a moment, not something this host observed.",
  },
];

function WatchPage() {
  const [params, setParams] = useSearchParams();
  const scope = params.get("view") === "fleet" ? "fleet" : "runs";

  function show(value: string) {
    const next = new URLSearchParams(params);
    if (value === "runs") next.delete("view");
    else next.set("view", value);
    setParams(next);
  }

  return (
    <section className="page watch-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Activity</p>
          <h1>What is it doing?</h1>
        </div>
      </div>

      <nav className="section-nav" aria-label="Scope">
        {SCOPES.map((entry) => (
          <button
            type="button"
            key={entry.value}
            className={scope === entry.value ? "active" : undefined}
            aria-pressed={scope === entry.value}
            title={entry.title}
            onClick={() => show(entry.value)}
          >
            {entry.label}
          </button>
        ))}
      </nav>

      {scope === "fleet" ? <FleetSection /> : <ExploreSection />}
    </section>
  );
}

export default WatchPage;
