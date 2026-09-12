import { NavLink, Outlet } from "react-router-dom";
import "../ask.css";

// Ask answers one question: what does Babel know, and what does it need from
// you?
//
// This is the Reality Ledger, renamed after what a reader does with it rather
// than after the store behind it. It holds four kinds of thing — the
// questions it is asking now, every question it has ever asked, its subjects,
// and what it believes about them — which is four destinations, and putting
// four more nouns in the primary row would turn a product's navigation back
// into a menu. §8.4 wants every stored thing reachable by moving through the
// navigation, so the depth lives here: this row is present on every page
// below it, including a belief or a question reached by clicking a record,
// which is where a reader most needs to know what else exists.
//
// The labels are the questions each destination answers, not the names of the
// stores behind them. "Questions / Asked / Subjects / Beliefs" required
// knowing the data model before you could pick one, and two of the four were
// nearly the same word. The store's own noun stays underneath, in small
// caps, because it is the word the CLI prints and the spec uses, and a reader
// moving between the two surfaces has to be able to join them up.
//
// Focus is no longer in this row. What Babel may spend on a subject is a
// ceiling the operator sets once and revises rarely, not something he comes
// here to read, so it sits with the other ceilings under Settings.
const destinations: { to: string; end?: boolean; question: string; noun: string }[] = [
  { to: "/ask", end: true, question: "What it needs", noun: "Questions" },
  { to: "/ask/questions", question: "What you said", noun: "Asked" },
  { to: "/ask/entities", question: "Who and what", noun: "Subjects" },
  { to: "/ask/facts", question: "What it believes", noun: "Beliefs" },
];

function AskPage() {
  return (
    <>
      <nav className="ask-nav" aria-label="Ask">
        {destinations.map((destination) => (
          <NavLink
            key={destination.to}
            end={destination.end}
            to={destination.to}
            className={({ isActive }) => (isActive ? "active" : undefined)}
          >
            <span className="ask-nav-question">{destination.question}</span>
            <span className="ask-nav-noun">{destination.noun}</span>
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </>
  );
}

export default AskPage;
