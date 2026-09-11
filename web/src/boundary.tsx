import { Component, type ErrorInfo, type ReactNode } from "react";

// A render fault must stay inside the page that caused it.
//
// React unmounts the whole tree when a render throws and nothing catches it,
// so before this existed a single bad record did not produce a broken panel:
// it produced an empty document. The operator saw a blank window, the server
// log showed the record's own fetch returning 200, and nothing anywhere named
// the failure. That is the worst shape a bug can take, because it destroys the
// evidence that would identify it.
//
// The boundary is keyed by route at the call site, so navigating away clears a
// fault rather than stranding the reader on a dead page, and the surrounding
// chrome — the navigation, the lock control — survives the fault that killed
// its child.
type Props = { children: ReactNode };
type State = { error: Error | null; stack: string };

class RenderBoundary extends Component<Props, State> {
  state: State = { error: null, stack: "" };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The component stack names the panel that failed, which the message
    // alone does not. It is kept in state rather than only logged, because an
    // operator reporting this should not have to open a console to describe
    // what broke.
    this.setState({ stack: info.componentStack ?? "" });
    console.error("render fault", error, info.componentStack);
  }

  render() {
    const { error, stack } = this.state;
    if (!error) return this.props.children;
    return (
      <section className="page">
        <div className="state-card error-state">
          <strong>This page could not be rendered.</strong>
          <span>
            The record was read successfully; displaying it is what failed, so nothing is
            lost. Every other page still works.
          </span>
          <pre className="fault-detail">
            {error.message}
            {stack ? `\n${stack.trim()}` : ""}
          </pre>
        </div>
      </section>
    );
  }
}

export default RenderBoundary;
