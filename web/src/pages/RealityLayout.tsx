import { NavLink, Outlet } from "react-router-dom";

// The Reality Ledger's own navigation.
//
// §8.4 requires every stored thing to be reachable by moving through the
// navigation, and the ledger stores five kinds of thing: the questions it is
// asking, every question it has ever asked, its subjects, what it believes
// about them, and the policy governing what analysis may spend on each. That is
// five destinations, and putting five more nouns in the primary row would turn
// a product's navigation into a menu.
//
// So the depth lives here instead. The primary row keeps one Reality entry, and
// this row — present on every page below it, including the ones reached by
// clicking a record — is how the operator moves between them. A reader who
// lands on a fact can see, without going back, that questions and subjects
// exist and where they are.
//
// Focus keeps its entry in the primary row as well, and that is deliberate
// rather than a duplicate: "stop spending on this" is an act an operator comes
// here to perform and must be able to find without being told where it is,
// which is exactly how it stayed a CLI-only capability for so long.
function RealityLayout() {
  return (
    <>
      <nav className="section-nav" aria-label="Reality Ledger">
        <NavLink end to="/reality" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Questions
        </NavLink>
        <NavLink to="/reality/questions" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Asked
        </NavLink>
        <NavLink to="/reality/entities" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Subjects
        </NavLink>
        <NavLink to="/reality/facts" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Beliefs
        </NavLink>
        <NavLink to="/reality/focus" className={({ isActive }) => (isActive ? "active" : undefined)}>
          Focus
        </NavLink>
      </nav>
      <Outlet />
    </>
  );
}

export default RealityLayout;
