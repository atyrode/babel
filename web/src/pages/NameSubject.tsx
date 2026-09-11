import { useEffect, useState, type FormEvent } from "react";
import {
  createSubject,
  getSubjectVocabulary,
  type SubjectAlias,
  type SubjectCreateResult,
  type SubjectVocabulary,
} from "../api";
import { errorMessage } from "../format";

// Naming a subject (SPEC.md §4.8, §8.4): the form that makes the ledger
// writable from a browser.
//
// It is one component used from two places because the need arises in two
// places and it is the same need. The Subjects listing is where an operator
// goes to see what Babel knows about, including when the answer is nothing;
// the Focus page's unresolved-name state is where he has just been told that
// the word he typed reaches no subject. Both used to end in a sentence telling
// him to go and run a CLI command, which §8.4 counts as a product that does
// not have the thing it stores.
//
// Three rules shape it.
//
// The vocabularies are the ledger's. The kinds a subject may be and the kinds
// of name it may answer to are closed sets §4.8 keeps closed so a typo is a
// refused write, and they are fetched rather than written down here: a list
// copied into this file would offer the operator a kind nothing can store.
//
// The word the operator already typed travels with him. A subject created
// without it as a typed name would leave the same word unresolvable the next
// time he types it, which is the dead end this form exists to close rather
// than to move by one step.
//
// It records an identity and nothing else. Naming a subject asserts no fact
// about it, and the form says so before the click and repeats the server's own
// sentence after it — because the operator is about to state a policy about
// this subject, and he has to know that the ledger currently believes nothing.

// TYPED_NAME_DEFAULT is the alias kind a word an operator typed is recorded
// under when he does not say otherwise. It is the weakest of the seven: "this
// is a name for it" claims nothing about where the name came from, while
// filing an unexplained word as a hostname or a repository would put a claim
// in the ledger that the operator did not make. It is still resolved against
// the fetched vocabulary rather than trusted.
const TYPED_NAME_DEFAULT = "name";

function NameSubjectForm({
  suggestedName = "",
  onCreated,
  onCancel,
}: {
  // suggestedName is the word the operator already typed, when he arrived
  // here from a lookup that found nothing. It becomes both the display name
  // and the first typed name, because that word is what he calls the thing.
  suggestedName?: string;
  onCreated: (result: SubjectCreateResult) => void;
  onCancel?: () => void;
}) {
  const [vocabulary, setVocabulary] = useState<SubjectVocabulary | null>(null);
  const [vocabularyError, setVocabularyError] = useState<string | null>(null);
  const [kind, setKind] = useState("");
  const [name, setName] = useState(suggestedName);
  const [notes, setNotes] = useState("");
  const [names, setNames] = useState<SubjectAlias[]>(
    suggestedName ? [{ kind: "", value: suggestedName }] : [],
  );
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  useEffect(() => {
    getSubjectVocabulary()
      .then((fetched) => {
        setVocabulary(fetched);
        // A row that was created before the vocabulary arrived has no kind
        // yet. It gets the weakest one the ledger actually offers, so the
        // default is the server's word rather than this file's.
        const preferred = fetched.alias_kinds.includes(TYPED_NAME_DEFAULT)
          ? TYPED_NAME_DEFAULT
          : (fetched.alias_kinds[0] ?? "");
        setNames((rows) => rows.map((row) => (row.kind ? row : { ...row, kind: preferred })));
      })
      .catch((reason) => setVocabularyError(errorMessage(reason)));
  }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!kind || !name.trim()) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      // A row the operator emptied is a name he decided against, so it is
      // dropped rather than sent: the ledger refuses a name with no value,
      // and answering a cleared field with a validation error would be
      // arguing with him about something he already withdrew.
      const typed = names
        .filter((row) => row.value.trim() !== "" && row.kind !== "")
        .map((row) => ({ kind: row.kind, value: row.value.trim() }));
      onCreated(
        await createSubject({ kind, name: name.trim(), notes: notes.trim(), aliases: typed }),
      );
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  if (vocabularyError) {
    return (
      <div className="state-card error-state">
        <strong>The ledger's own vocabulary could not be read.</strong>
        <span>{vocabularyError}</span>
        <span className="muted">
          A subject has to be one of the kinds the ledger accepts, and this page will not
          guess at the list.
        </span>
      </div>
    );
  }

  return (
    <form className="focus-form name-subject" onSubmit={submit}>
      <label>
        What do you call it?
        <input
          type="text"
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="the name you would use for it out loud"
        />
      </label>
      <label>
        What is it?
        <select value={kind} onChange={(event) => setKind(event.target.value)}>
          <option value="">choose a kind…</option>
          {(vocabulary?.kinds ?? []).map((option) => (
            <option key={option} value={option}>{option}</option>
          ))}
        </select>
      </label>

      <fieldset>
        <legend>Names it also answers to</legend>
        <p className="muted">
          Typed names are how Babel recognizes this subject later: a rename, a path, a
          repository, a hostname, or the word you use for it in conversation all reach one
          identity. A name recorded here is what makes the subject findable by that word —
          the name above is a label, not a way in.
        </p>
        {names.map((row, index) => (
          <div className="name-subject-name" key={index}>
            <label className="kind">
              Kind
              <select
                value={row.kind}
                onChange={(event) =>
                  setNames((rows) =>
                    rows.map((other, at) =>
                      at === index ? { ...other, kind: event.target.value } : other,
                    ),
                  )
                }
              >
                {(vocabulary?.alias_kinds ?? []).map((option) => (
                  <option key={option} value={option}>{option}</option>
                ))}
              </select>
            </label>
            <label className="value">
              Name
              <input
                type="text"
                value={row.value}
                onChange={(event) =>
                  setNames((rows) =>
                    rows.map((other, at) =>
                      at === index ? { ...other, value: event.target.value } : other,
                    ),
                  )
                }
                placeholder="clear this to leave it out"
              />
            </label>
          </div>
        ))}
        <div className="focus-actions">
          <button
            type="button"
            onClick={() =>
              setNames((rows) => [
                ...rows,
                {
                  kind: vocabulary?.alias_kinds.includes(TYPED_NAME_DEFAULT)
                    ? TYPED_NAME_DEFAULT
                    : (vocabulary?.alias_kinds[0] ?? ""),
                  value: "",
                },
              ])
            }
            disabled={!vocabulary}
          >
            Add another name
          </button>
        </div>
      </fieldset>

      <label>
        What is it, in your words? (optional, kept with the subject)
        <input
          type="text"
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
          placeholder="a sentence a future reader would want"
        />
      </label>

      <p className="muted">
        This records the subject and the names above. It states nothing about it: no facts,
        no questions, no analysis — those come from what you or a run says next.
      </p>
      <div className="focus-actions">
        <button type="submit" className="primary-button" disabled={submitting || !kind || !name.trim()}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : "Name this subject"}
        </button>
        {onCancel && (
          <button type="button" onClick={onCancel} disabled={submitting}>Cancel</button>
        )}
      </div>
      {submitError && <p className="inline-error" role="alert">{submitError}</p>}
    </form>
  );
}

export default NameSubjectForm;
