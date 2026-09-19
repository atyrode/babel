import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, door } from "../contract.ts";
import {
  SERVICE_STATE_WORD,
  serviceState,
  type ServicePreview,
  type ServicesPreview,
} from "./api.ts";

/*
  THE SERVICES BABEL BINDS — the policy, before it lands (#400).

  Installing one was a hand-written owner call to `engine.services.configureConfiguration` with
  its arguments spelled out in `docs/runbook.md` §4. This is that call as a screen: the policy
  composed out of the manifest's own binding, shown with its digest, and installed as a
  compare-and-swap on the revision it was read at.

  THE ONE FIELD IS AN ENDPOINT, AND THERE WILL NOT BE A SECOND ONE. A credential value has no
  path anywhere in Manifold — the machine's agent reads it off its own disk and the protocol
  advertises "references and allowed origins, never source paths or values" — so a paste field
  here would mean the key transiting Babel's server half, which is exactly what naming a
  credential by reference removed. What this panel does instead is NAME THE FILE: the reference
  the policy needs and the path on that machine the agent will read it from, so the manual step
  is one the product states rather than one the operator has to already know.

  AND IT SAYS WHETHER THE BINDING CAME UP. Until now, a machine with no policy and a machine
  whose policy is installed and whose key is not there produced the same silence: the archive
  refuses and nothing on any screen says which of the two it was. The word beside each service
  is that distinction — `not configured`, `configured, not ready`, `ready` — and the sentence
  under it is the remedy for whichever one it is.
*/

export interface ServicesProps {
  readonly preview: ServicesPreview | null;
  /** What the operator has typed per service; empty keeps whatever origin is installed. */
  readonly drafts: Readonly<Record<string, string>>;
  readonly machines: readonly MachineSummary[];
  readonly machineId: string;
  readonly checking: boolean;
  readonly installing: boolean;
  /** A failed read, or what the last install said. */
  readonly note: string;
  onMachine(machineId: string): void;
  onDraft(serviceId: string, origin: string): void;
  onCheck(): void;
  onInstall(): void;
}

/** One declared service: where it stands, what it would be, and what is owed. */
function Service({
  service,
  draft,
  onDraft,
}: {
  readonly service: ServicePreview;
  readonly draft: string;
  onDraft(serviceId: string, origin: string): void;
}) {
  const state = serviceState(service);
  return (
    <Stack
      gap="var(--babel-space-2)"
      className="plugin-atyrode_babel_watch__drain"
      data-service={service.serviceId}
    >
      <Cluster gap="var(--babel-space-2)">
        <span className="plugin-atyrode_babel_watch__mono">{service.serviceId}</span>
        <span className="plugin-atyrode_babel_watch__chip" data-field="state">
          {SERVICE_STATE_WORD[state]}
        </span>
        <span className="plugin-atyrode_babel_watch__muted">
          revision {service.revision} · {service.operations.join(", ")}
        </span>
      </Cluster>
      {service.reason === "" ? null : (
        <p className="plugin-atyrode_babel_watch__muted" data-field="reason">
          {service.reason}
        </p>
      )}
      {/*
        THE FILE, ALWAYS, AND NEVER A FIELD FOR WHAT GOES IN IT. It is shown whatever the state,
        because an operator reading a ready service still needs to know where its key lives when
        he rebuilds the machine, and a `ready` that hid the path would send him back to a
        runbook for the one fact only this screen can state.
      */}
      <p className="plugin-atyrode_babel_watch__muted" data-field="credential">
        The policy names the credential <code>{service.credential.ref}</code> and never its value.
        Write the token to <code>{service.credential.file}</code> on that machine; the hub{" "}
        {service.credential.advertised
          ? service.credential.readable
            ? "reads it there."
            : "sees the name and cannot use it here."
          : "does not see it yet."}
      </p>
      <label className="plugin-atyrode_babel_watch__knob">
        <span className="plugin-atyrode_babel_watch__knob-label">Endpoint</span>
        <input
          type="url"
          className="plugin-atyrode_babel_watch__field"
          data-field="origin"
          placeholder={service.origin === "" ? "https://…" : service.origin}
          value={draft}
          onInput={(event) => onDraft(service.serviceId, event.currentTarget.value)}
        />
      </label>
    </Stack>
  );
}

export function Services({
  preview,
  drafts,
  machines,
  machineId,
  checking,
  installing,
  note,
  onMachine,
  onDraft,
  onCheck,
  onInstall,
}: ServicesProps) {
  /*
    AN INSTALL IS OFFERED ONLY WHEN THERE IS SOMETHING TO INSTALL, and each way there is not
    says which. The load-bearing one is the fourth: an endpoint typed since the last Check is
    not in the preview, so the digest the press would carry is the digest of a policy pointing
    somewhere else — the door refuses exactly that, and offering the button would be inviting
    a refusal the panel could see coming. Typing then re-reading IS the three-step.
  */
  const drifted = preview?.services.some((service) => {
    const draft = (drafts[service.serviceId] ?? "").trim();
    return draft !== "" && draft !== service.origin;
  });
  const blocked =
    machineId === ""
      ? "Pick a machine."
      : preview === null
        ? "Read the policy first."
        : !preview.connected
          ? `The hub cannot reach ${machineId}.`
          : drifted === true
            ? "Press Check to compose the policy with that endpoint."
            : (preview.services.find((service) => service.origin === "")?.reason ??
              (preview.current ? "The composed policy is already what stands there." : ""));
  return (
    <Stack
      gap="var(--babel-space-3)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__services-section"
    >
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Services</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          The host services Babel&rsquo;s operations bind, composed from the manifest that declares
          them. Read the policy, then install it against the revision you read — a policy that moved
          underneath is refused rather than overwritten.
        </p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__open">
        <Cluster gap="var(--babel-space-4)" className="plugin-atyrode_babel_watch__knobs">
          <label className="plugin-atyrode_babel_watch__knob">
            <span className="plugin-atyrode_babel_watch__knob-label">Machine</span>
            <select
              className="plugin-atyrode_babel_watch__picker"
              data-field="machine"
              value={machineId}
              onChange={(event) => onMachine(event.target.value)}
            >
              <option value="">Pick a machine…</option>
              {machines.map((machine) => (
                <option key={machine.id} value={machine.id}>
                  {machine.name}
                  {machine.online ? "" : " · offline"}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            className="plugin-atyrode_babel_watch__quiet"
            data-action={door(ACTIONS.previewServices)}
            disabled={checking || machineId === ""}
            onClick={onCheck}
          >
            {checking ? "Checking…" : "Check"}
          </button>
        </Cluster>
        {preview === null ? (
          <p className="plugin-atyrode_babel_watch__muted" data-field="unread">
            Nothing has been read yet. Pick a machine and press Check.
          </p>
        ) : (
          preview.services.map((service) => (
            <Service
              key={service.serviceId}
              service={service}
              draft={drafts[service.serviceId] ?? ""}
              onDraft={onDraft}
            />
          ))
        )}
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__primary"
            data-action={door(ACTIONS.installServices)}
            disabled={installing || blocked !== ""}
            onClick={onInstall}
          >
            {installing ? "Installing…" : "Install"}
          </button>
          {blocked === "" ? null : (
            <span className="plugin-atyrode_babel_watch__muted">{blocked}</span>
          )}
          {/*
            THE DIGEST IS ON THE SCREEN because it is what the press carries: the install echoes
            it, and a preview the operator did not re-read after something moved is refused by
            name. Showing it is how "compare-and-swap" is a thing he can see rather than a claim.
          */}
          {preview === null ? null : (
            <span className="plugin-atyrode_babel_watch__mono" data-field="digest">
              {preview.previewDigest.slice(0, 12)} ·{" "}
              {preview.expectedRevision === null
                ? "no configuration"
                : preview.expectedRevision.slice(0, 12)}
            </span>
          )}
        </Cluster>
      </Stack>
    </Stack>
  );
}
