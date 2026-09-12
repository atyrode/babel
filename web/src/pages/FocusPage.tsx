import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  assertFocusPolicy,
  getFocus,
  getFocusSubject,
  installFocusPolicy,
  supersedeFocusPolicy,
  type FocusChoice,
  type FocusResponse,
  type FocusRuleInForce,
  type FocusSubject,
  type FocusSubjectResponse,
  type SubjectCreateResult,
} from "../api";
import NameSubjectForm from "./NameSubject";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, type Tone } from "../analysis";

// What Babel is allowed to spend on each subject (SPEC.md §4.8), and the page
// where an operator changes it.
//
// The product rule this page exists for: the answer to "stop exploring this
// dead project" has to be reachable by clicking. It used to be a CLI
// incantation — install a rule set version, then assert an analysis-policy
// fact against a canonical entity identifier — and nobody remembers that.
//
// Three rules shape how it renders.
//
// A consequence is never paraphrased. Every allowance arrives with the
// server's own sentence about what it withholds and what it keeps, and that
// sentence is shown verbatim next to the control that chooses it. The operator
// must not need §4.8 open to pick correctly, and a wording invented here would
// drift from the one a deferral is explained with.
//
// A subject is named the way the operator names it. The picker takes a word,
// not an identifier, and the ledger's aliases resolve it — the same resolution
// a run performs when it meets "dev-01" in a transcript.
//
// Nothing is deleted. Lifting a restriction writes a later revision, the
// earlier one stays readable, and the page says so: the control is "revise",
// the history is shown, and there is no button anywhere here that removes a
// record.

// ALLOWANCE_TONES colours what is withheld. Excluded is red because it is the
// strongest thing an operator can say and he has to be able to see it at a
// glance; full is green because it is the absence of a restriction rather than
// a fourth kind of one.
const ALLOWANCE_TONES: Record<string, Tone> = {
  full: "green",
  "learn-only": "cyan",
  "no-code-investigation": "amber",
  excluded: "red",
};

function FocusPage() {
  const [params, setParams] = useSearchParams();
  const [state, setState] = useState<FocusResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const [installing, setInstalling] = useState(false);
  const [installError, setInstallError] = useState<string | null>(null);
  // revision counts the writes this page has performed, and it is what makes
  // the subject panel honest after one. A revision recorded from a rule card
  // leaves the panel above holding the fact it resolved a moment ago, which is
  // now superseded — and a stale panel offering to revise a policy that has
  // already moved is exactly the state the server refuses. The refusal is the
  // safety net; re-resolving is not needing it.
  const [revision, setRevision] = useState(0);

  const load = useCallback((mode: "blocking" | "quiet") => {
    if (mode === "blocking") {
      setLoading(true);
      setError(null);
    }
    getFocus()
      .then(setState)
      .catch((reason) => {
        if (mode === "blocking") setError(errorMessage(reason));
      })
      .finally(() => {
        if (mode === "blocking") setLoading(false);
      });
  }, []);

  useEffect(() => load("blocking"), [load]);

  // changed is what every write on this page reports through: the spoken
  // announcement, the reload of what is in force, and the bump that makes
  // every other panel re-read the subject it is showing.
  const changed = useCallback(
    (message: string) => {
      setAnnouncement(message);
      setRevision((count) => count + 1);
      load("quiet");
    },
    [load],
  );

  async function install() {
    setInstalling(true);
    setInstallError(null);
    try {
      const result = await installFocusPolicy();
      setAnnouncement(
        `Focus policy version ${result.policy?.version ?? ""} installed. ` +
          `It applies to ${result.applies}.`,
      );
      load("quiet");
    } catch (reason) {
      setInstallError(errorMessage(reason));
    } finally {
      setInstalling(false);
    }
  }

  const withheld = state?.rules.filter((rule) => rule.withholds) ?? [];
  const lifted = state?.rules.filter((rule) => !rule.withholds) ?? [];

  return (
    // A section of Settings rather than a page of its own: a ceiling on what
    // analysis may spend on a subject is something the operator states once
    // and revises rarely, which is what Settings is for. It is still an
    // attributed fact in the ledger and still reversible — see below.
    <div className="page-section focus-section">
      {state && (
        <p className="count-label section-meta">
          {withheld.length} {withheld.length === 1 ? "restriction" : "restrictions"}
        </p>
      )}

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {loading && !state && (
        <div className="surface state-note"><span className="spinner" /> Reading the focus policy…</div>
      )}
      {error && !state && (
        <div className="surface state-note error-state">
          <strong>The focus policy could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load("blocking")}>Try again</button>
        </div>
      )}

      {state && !state.installed && (
        <div className="surface state-note focus-install">
          <strong>No focus policy is installed, so nothing is withheld.</strong>
          <span>{state.note}</span>
          <span className="muted">
            The policy is the versioned mapping from what you have said about a subject to
            what analysis may spend on it. Version {state.shipped_version} maps a stated
            analysis policy and nothing else: a dormant project is not by itself a project
            Babel may not spend on, so lifecycle carries no expenditure meaning in it.
          </span>
          <button type="button" className="primary-button" onClick={install} disabled={installing}>
            {installing && <span className="spinner small" />}
            {installing ? "Installing…" : `Install version ${state.shipped_version}`}
          </button>
          {installError && <p className="inline-error" role="alert">{installError}</p>}
          {state.stated.length > 0 && (
            <div className="focus-stated">
              <p className="eyebrow">Stated already, and not in force</p>
              <ul>
                {state.stated.map((stated) => (
                  <li key={stated.fact.id}>
                    <SubjectName subject={stated.subject} />
                    <span className="mono"> {stated.policy}</span>
                    <span className="muted"> — recorded, and interpreted by nothing</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {state?.installed && (
        <SubjectPicker
          choices={state.choices}
          term={params.get("subject") ?? ""}
          onTerm={(value) => {
            const next = new URLSearchParams(params);
            if (value) next.set("subject", value);
            else next.delete("subject");
            setParams(next, { replace: true });
          }}
          revision={revision}
          onChanged={changed}
        />
      )}

      {state?.installed && (
        <article className="surface focus-rules">
          <div className="section-heading">
            <div>
              <p className="eyebrow">In force</p>
              <h2>Subjects Babel is holding back on</h2>
            </div>
            <span className="count-label">{withheld.length}</span>
          </div>
          {withheld.length === 0 ? (
            <p className="muted">
              Nothing is withheld. Babel spends on every subject it reaches, within whatever
              each run was granted.
            </p>
          ) : (
            <div className="focus-rule-list">
              {withheld.map((rule) => (
                <RuleCard
                  key={rule.fact.id}
                  rule={rule}
                  choices={state.choices}
                  onChanged={changed}
                />
              ))}
            </div>
          )}
        </article>
      )}

      {lifted.length > 0 && state && (
        <article className="surface focus-rules">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Stated, withholding nothing</p>
              <h2>Restrictions you have lifted</h2>
            </div>
            <span className="count-label">{lifted.length}</span>
          </div>
          <p className="muted">
            These subjects carry a policy that withholds nothing. The revisions that once
            restricted them are still readable — a lifted restriction is a later fact, never
            a deleted one.
          </p>
          <div className="focus-rule-list">
            {lifted.map((rule) => (
              <RuleCard
                key={rule.fact.id}
                rule={rule}
                choices={state.choices}
                onChanged={changed}
              />
            ))}
          </div>
        </article>
      )}

      {state?.policy && (
        <article className="surface focus-policy">
          <div className="section-heading">
            <div>
              <p className="eyebrow">The mapping</p>
              <h2>Policy version {state.policy.version}</h2>
            </div>
            <span className="count-label">
              {formatTime(state.policy.installed_at)?.relative ?? "installed"}
            </span>
          </div>
          {state.policy.note && <p className="muted">{state.policy.note}</p>}
          <div className="focus-choice-list">
            {state.choices.map((choice) => (
              <div key={choice.policy} className="focus-choice">
                <div className="focus-choice-head">
                  <span className="mono">{choice.policy}</span>
                  <span aria-hidden="true">→</span>
                  <Badge label={choice.allowance} tone={ALLOWANCE_TONES[choice.allowance] ?? "neutral"} />
                </div>
                <p>{choice.means}</p>
                {choice.conditional && (
                  <p className="muted">
                    This version also matches on other facts about a subject, so a particular
                    subject's decision may differ.
                  </p>
                )}
              </div>
            ))}
          </div>
          <details className="focus-rule-source">
            <summary>The {state.policy.rules.length} rules, in evaluation order</summary>
            <ol>
              {state.policy.rules.map((rule) => (
                <li key={rule.name}>
                  <span className="mono">{rule.name}</span>
                  {rule.when.length === 0 ? (
                    <span className="muted"> matches everything</span>
                  ) : (
                    <span className="muted">
                      {" "}when {rule.when.map((cond) => `${cond.predicate} = ${cond.equals}`).join(" and ")}
                    </span>
                  )}
                  <span> → </span>
                  <Badge label={rule.allows} tone={ALLOWANCE_TONES[rule.allows] ?? "neutral"} />
                  <p className="muted">{rule.because}</p>
                </li>
              ))}
            </ol>
            <p className="muted">
              First match wins. When no rule matches, the version's default applies:{" "}
              <span className="mono">{state.policy.default}</span> — {state.policy.default_means}
            </p>
          </details>
        </article>
      )}
    </div>
  );
}

// SubjectName renders the entity a rule is about: its display name, the names
// it is also known by, and a link to its record. The names are the operator's
// own vocabulary and render as quoted untrusted text; the link is built from
// the entity id and never from anything inside the name.
function SubjectName({ subject }: { subject: FocusSubject }) {
  return (
    <span className="focus-subject">
      <Link className="untrusted-inline" to={`/ask/entities/${encodeURIComponent(subject.entity_id)}`}>
        {subject.display_name}
      </Link>
      {subject.aliases.length > 0 && (
        <span className="muted focus-aliases">
          {" also "}
          {subject.aliases.slice(0, 3).map((alias) => (
            <span key={alias} className="untrusted-inline">{alias}</span>
          ))}
        </span>
      )}
    </span>
  );
}

// SubjectPicker is the way in that does not require knowing a canonical entity
// name: a word, resolved through the ledger's aliases, then the policy.
//
// The resolution is a separate step rather than folded into the write, and
// deliberately so. It has two failure modes the operator has to see before a
// fact is written in his name — a name the ledger does not know, and a name
// that means two entities — and it is also what tells him whether he is
// stating a policy for the first time or revising one he already stated.
function SubjectPicker({
  choices,
  term,
  revision,
  onTerm,
  onChanged,
}: {
  choices: FocusChoice[];
  term: string;
  revision: number;
  onTerm: (value: string) => void;
  onChanged: (message: string) => void;
}) {
  const [input, setInput] = useState(term);
  const [found, setFound] = useState<FocusSubjectResponse | null>(null);
  const [looking, setLooking] = useState(false);
  const [lookupError, setLookupError] = useState<string | null>(null);
  // naming is the dead end turned into the next step: the operator typed a
  // word, the ledger answered that nothing has it, and this is the form that
  // gives the word a subject. It is closed by default rather than always
  // shown, because the ordinary answer to "nothing matched" is a typo.
  const [naming, setNaming] = useState(false);

  const resolve = useCallback((value: string) => {
    setLooking(true);
    setLookupError(null);
    setFound(null);
    // A fresh lookup closes the naming form. It was opened about a word that
    // reached nothing, and leaving it open over a different answer would
    // offer to name a subject for a word that now resolves.
    setNaming(false);
    getFocusSubject(value)
      .then(setFound)
      .catch((reason) => setLookupError(errorMessage(reason)))
      .finally(() => setLooking(false));
  }, []);

  // A subject named in the URL is resolved on arrival, which is what makes
  // "stop spending on this" reachable from a record page: the link carries the
  // word, and this page answers it without the operator retyping it.
  //
  // It re-resolves after every write the page performs, including one made
  // from a rule card below. Its own writes are covered by the same path, so
  // there is one rule rather than two: whatever the ledger now says about this
  // subject is what this panel shows.
  useEffect(() => {
    if (term) resolve(term);
  }, [term, revision, resolve]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const value = input.trim();
    if (!value) return;
    onTerm(value);
    resolve(value);
  }

  // named lands the operator where he was going. He came here to state a
  // policy about something, was told the word reaches nothing, and has just
  // given the word a subject — so the panel re-resolves and shows the control
  // he came for instead of a confirmation he has to act on again.
  //
  // Which word it re-resolves is the ledger's business rather than a guess.
  // A typed name normalizes by trimming and lowercasing, so if the word the
  // operator typed was recorded as one of the subject's names, that word now
  // reaches it and stays in the box; if he cleared that row, nothing resolves
  // it and the canonical identifier is what the panel asks about — the same
  // identifier an entity page links here with.
  function named(result: SubjectCreateResult) {
    const typed = found?.term ?? "";
    const recorded = result.aliases.some(
      (alias) => alias.value.trim().toLowerCase() === typed.trim().toLowerCase(),
    );
    const next = recorded ? typed : result.subject.entity_id;
    setNaming(false);
    setInput(next);
    onTerm(next);
    // onChanged bumps the page's revision, and the effect above re-resolves
    // on either that or the new term, so the panel re-reads exactly once.
    onChanged(`${result.subject.display_name} is now a subject in the ledger. ${result.believes}.`);
  }

  return (
    <article className="surface focus-picker">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Change what is spent</p>
          <h2>Find a subject</h2>
        </div>
      </div>
      <form className="focus-lookup" onSubmit={submit}>
        <label>
          A name you use for it
          <input
            type="text"
            value={input}
            onChange={(event) => setInput(event.target.value)}
            placeholder="a project name, a repository, a hostname, or what you call it in chat"
          />
        </label>
        <button type="submit" className="primary-button" disabled={looking || !input.trim()}>
          {looking && <span className="spinner small" />}
          {looking ? "Looking…" : "Find"}
        </button>
      </form>
      <p className="muted">
        Any name the ledger knows works — a rename, a path, a repository, or the word you use
        for it in conversation. Babel resolves it the same way a run does.
      </p>
      {lookupError && <p className="inline-error" role="alert">{lookupError}</p>}

      {found && !found.resolved && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing matched that name.</strong>
          <span>{found.reason}</span>
          {/* Naming is offered for the word that reaches nothing and withheld
              for the word that already means several subjects. The server
              decides which, because it is the same distinction that decides
              whether the creation would be refused: a third thing answering
              to an ambiguous word makes the resolution the operator owes the
              ledger worse. */}
          {found.nameable && !naming && (
            <button type="button" className="primary-button" onClick={() => setNaming(true)}>
              Name <span className="untrusted-inline">{found.term}</span> as a subject
            </button>
          )}
          {found.nameable && naming && (
            <NameSubjectForm
              suggestedName={found.term}
              onCreated={named}
              onCancel={() => setNaming(false)}
            />
          )}
        </div>
      )}

      {found?.resolved && found.subject && (
        <div className="focus-found">
          <div className="focus-found-head">
            <SubjectName subject={found.subject} />
            <Badge label={found.subject.kind} tone="cyan" />
          </div>
          {found.rule ? (
            <>
              <p>
                A policy is already in force for this subject:{" "}
                <span className="mono">{found.rule.policy}</span>, which means{" "}
                <Badge
                  label={found.rule.allowance}
                  tone={ALLOWANCE_TONES[found.rule.allowance] ?? "neutral"}
                />
              </p>
              <p className="muted">{found.rule.means}</p>
              <PolicyForm
                choices={choices}
                current={found.rule.policy}
                action="revise"
                submitLabel="Revise the policy"
                onSubmit={(policy, note) => supersedeFocusPolicy(found.rule!.fact.id, policy, note)}
                onChanged={onChanged}
              />
            </>
          ) : (
            <>
              <p className="muted">
                Nothing is withheld for this subject yet. Stating a policy records your own
                fact about it, attributed to you, in force until you supersede it.
              </p>
              <PolicyForm
                choices={choices}
                action="assert"
                submitLabel="State this policy"
                onSubmit={(policy, note) => assertFocusPolicy(found.subject!.entity_id, policy, note)}
                onChanged={onChanged}
              />
            </>
          )}
          {found.history.length > 0 && (
            <details className="focus-history">
              <summary>
                {found.history.length} {found.history.length === 1 ? "revision" : "revisions"} of
                this subject's policy
              </summary>
              <ol>
                {found.history.map((fact) => {
                  const recorded = formatTime(fact.recorded_at);
                  return (
                    <li key={fact.id} className={`status-${fact.status}`}>
                      <span className="mono">{fact.value.enum}</span>
                      <Badge label={fact.status} tone={fact.status === "active" ? "green" : "neutral"} />
                      <span className="muted">
                        {" by "}{fact.authority.id}
                        {recorded && <> · <time title={recorded.absolute}>{recorded.relative}</time></>}
                      </span>
                      {fact.note && <Quoted label="Reason given" text={fact.note} />}
                    </li>
                  );
                })}
              </ol>
            </details>
          )}
        </div>
      )}
    </article>
  );
}

// RuleCard is one standing decision: what is withheld, why, and the fact it
// derives from.
//
// The fact is shown rather than summarized. A restriction an operator cannot
// trace to something he said is one he cannot argue with, and §4.8's whole
// mechanism is that the decision names the rule and the facts it read.
function RuleCard({
  rule,
  choices,
  onChanged,
}: {
  rule: FocusRuleInForce;
  choices: FocusChoice[];
  onChanged: (message: string) => void;
}) {
  const [revising, setRevising] = useState(false);
  const recorded = formatTime(rule.fact.recorded_at);

  return (
    <div className={`focus-rule allowance-${rule.allowance}`}>
      <div className="focus-rule-head">
        <SubjectName subject={rule.subject} />
        <Badge label={rule.allowance} tone={ALLOWANCE_TONES[rule.allowance] ?? "neutral"} />
        {rule.contested && <Badge label="contested" tone="amber" />}
      </div>
      <p className="focus-means">{rule.means}</p>
      <p className="muted focus-because">
        {rule.because}
        {rule.rule && <> · rule <span className="mono">{rule.rule}</span></>}
      </p>
      {rule.contested && (
        <p className="inline-warning">
          A fact this decision depends on is stale or disputed, so it rests on shaky input.
          Open the subject's record to see which.
        </p>
      )}
      <p className="muted focus-provenance">
        Stated as <span className="mono">{rule.policy}</span> by {rule.fact.authority.id}
        {recorded && <> · <time title={recorded.absolute}>{recorded.relative}</time></>}
        {" · fact "}
        <span className="mono">{rule.fact.id}</span>
      </p>
      {rule.fact.note && <Quoted label="Reason given" text={rule.fact.note} />}
      {revising ? (
        <PolicyForm
          choices={choices}
          current={rule.policy}
          action="revise"
          submitLabel="Revise the policy"
          onSubmit={(policy, note) => supersedeFocusPolicy(rule.fact.id, policy, note)}
          onChanged={(message) => {
            setRevising(false);
            onChanged(message);
          }}
          onCancel={() => setRevising(false)}
        />
      ) : (
        <button type="button" onClick={() => setRevising(true)}>
          {rule.withholds ? "Change or lift this…" : "Change this…"}
        </button>
      )}
    </div>
  );
}

// PolicyForm chooses a policy and states why.
//
// Every option carries the consequence sentence for the allowance it maps to,
// and the selected one is repeated in full under the control: the operator is
// choosing an outcome, not a vocabulary word, and the outcome is the thing that
// has to be legible at the moment of the click.
//
// The reason is optional and the field says so. §4.8 keeps the note in the
// fact's payload, so it is provenance for a future reader rather than a form
// field to satisfy — and a required box would be answered with "x".
function PolicyForm({
  choices,
  current,
  action,
  submitLabel,
  onSubmit,
  onChanged,
  onCancel,
}: {
  choices: FocusChoice[];
  current?: string;
  action: "assert" | "revise";
  submitLabel: string;
  onSubmit: (policy: string, note: string) => Promise<{ rule: FocusRuleInForce | null; note?: string }>;
  onChanged: (message: string) => void;
  onCancel?: () => void;
}) {
  const offered = choices.filter((choice) => choice.policy !== current);
  const [policy, setPolicy] = useState(offered[0]?.policy ?? "");
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const chosen = choices.find((choice) => choice.policy === policy);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!policy) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const result = await onSubmit(policy, note);
      const allowance = result.rule?.allowance ?? "";
      onChanged(
        action === "assert"
          ? `Policy ${policy} stated. ${allowance ? `Analysis is now ${allowance} on this subject.` : result.note ?? ""}`
          : `Policy revised to ${policy}. ${allowance ? `Analysis is now ${allowance} on this subject.` : result.note ?? ""}`,
      );
      setNote("");
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  if (offered.length === 0) {
    return <p className="muted">This is the only policy this version can act on.</p>;
  }

  return (
    <form className="focus-form" onSubmit={submit}>
      <fieldset>
        <legend>{action === "assert" ? "What should Babel spend on it?" : "What should change?"}</legend>
        {offered.map((choice) => (
          <label key={choice.policy} className="focus-option">
            <input
              type="radio"
              name={`policy-${action}-${current ?? "new"}`}
              value={choice.policy}
              checked={policy === choice.policy}
              onChange={() => setPolicy(choice.policy)}
            />
            <span className="focus-option-body">
              <span className="focus-option-head">
                <span className="mono">{choice.policy}</span>
                <Badge label={choice.allowance} tone={ALLOWANCE_TONES[choice.allowance] ?? "neutral"} />
                {!choice.withholds && <span className="muted">lifts every restriction</span>}
              </span>
              <span className="focus-option-means">{choice.means}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <label>
        Why (optional, kept with the fact)
        <input
          type="text"
          value={note}
          onChange={(event) => setNote(event.target.value)}
          placeholder="a sentence a future reader would want"
        />
      </label>
      {chosen && (
        <p className="focus-confirm">
          <strong>{chosen.allowance}:</strong> {chosen.means}
        </p>
      )}
      <div className="focus-actions">
        <button type="submit" className="primary-button" disabled={submitting || !policy}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : submitLabel}
        </button>
        {onCancel && (
          <button type="button" onClick={onCancel} disabled={submitting}>Cancel</button>
        )}
      </div>
      <p className="muted">
        This records a fact attributed to you. It supersedes rather than deletes: whatever is
        in force now stays readable afterwards.
      </p>
      {submitError && <p className="inline-error" role="alert">{submitError}</p>}
    </form>
  );
}

export default FocusPage;
