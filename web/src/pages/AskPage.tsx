import { NavLink, Outlet } from "react-router-dom";

// Ask answers one question: what does Babel know, and what does it need from
// you?
//
// This is the Reality Ledger, renamed after what a reader does with it rather
// than after the store behind it, and otherwise unchanged. It holds four kinds
// of thing — the questions it is asking now, every question it has ever asked,
// its subjects, and what it believes about them — which is four destinations,
// and putting four more nouns in the primary row would turn a product's
// navigation back into a menu. §8.4 wants every stored thing reachable by
// moving through the navigation, so the depth lives here: this row is present
// on every page below it, including a belief or a question reached by clicking
// a record, which is where a reader most needs to know what else exists.
//
// Focus is no longer in this row. What Babel may spend on a subject is a
// ceiling the operator sets once and revises rarely, not something he comes
// here to read, so it sits with the other ceilings under Settings.
function AskPage() {
  return (
    <>
      <nav className="section-nav" aria-label="Ask">
        <NavLink end to="/ask" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Questions
        </NavLink>
        <NavLink to="/ask/questions" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Asked
        </NavLink>
        <NavLink to="/ask/entities" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Subjects
        </NavLink>
        <NavLink to="/ask/facts" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Beliefs
        </NavLink>
      </nav>
      <Outlet />
    </>
  );
}

export default AskPage;
