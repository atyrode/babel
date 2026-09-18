import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";

/*
  MOUNT, DRIVE, UNMOUNT — the panel exercised the way the shell exercises it.

  Every verb here folds through `act`, so an effect a click schedules (a door call, a state fold,
  a timer registration) has run by the time the assertion reads the DOM. The event dispatches are
  the browser's own: React 19 delegates from the root container, so a real `click()` and a real
  `change` reach the handler exactly as they do in a browser — which is the point of testing the
  panel rather than a reducer pretending to be one.
*/

const mounted: Array<{ readonly root: Root; readonly host: HTMLElement }> = [];

export async function mount(element: ReactElement): Promise<HTMLElement> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ root, host });
  await act(async () => {
    root.render(element);
  });
  return host;
}

export async function unmountAll(): Promise<void> {
  for (const entry of mounted.splice(0)) {
    await act(async () => {
      entry.root.unmount();
    });
    entry.host.remove();
  }
}

/** Lets pending promises and timers land: a poll answering, an interval firing. */
export async function settle(ms = 0): Promise<void> {
  await act(async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, ms);
    await promise;
  });
}

export async function click(element: Element | null): Promise<void> {
  if (element === null) throw new Error("click: no such element");
  // A rendered control is an HTMLElement; nothing runtime-checkable is being claimed.
  const control = element as HTMLElement;
  await act(async () => {
    control.click();
  });
}

/** Picks an option the way a pointer does: the value, then the `change` React listens for. */
export async function choose(select: Element | null, value: string): Promise<void> {
  if (select === null) throw new Error("choose: no such select");
  const field = select as HTMLSelectElement;
  await act(async () => {
    field.value = value;
    field.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

/**
 * Types a number into a controlled input.
 *
 * The prototype setter rather than `field.value = …` because React tracks the last value it
 * wrote on the node: assigning through the instance property updates that tracker too, and the
 * `input` event is then discarded as a no-op change.
 */
export async function type(field: Element | null, value: string): Promise<void> {
  if (field === null) throw new Error("type: no such input");
  const input = field as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  if (setter === undefined) throw new Error("type: no value setter on HTMLInputElement");
  await act(async () => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
