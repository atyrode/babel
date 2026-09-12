import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import RenderBoundary from "../boundary";
import { errorMessage } from "../format";
import { RecordHeading, RecordPeels, RecordThread } from "../record";
import { getRecord, type OperatorReception, type RecordPeel } from "../recordapi";

// One record, at whatever depth the reader wants it.
//
// This page replaces four: a proposal was read at /proposals/:id, ruled on at
// /review/proposal/:id, its reception was at /evaluation/proposal/:id and its
// citations at neither. The operator's own account of that was "it's all
// flattened into one big layer of features instead of being coherently
// assembled together" — four surfaces each holding a quarter of one object.
//
// So the page is the object. It fetches once, and the whole record arrives:
// the claim, the case, the evidence, the reception and the machinery. Digging
// is a disclosure, never a request and never a route, which is what makes
// "read the evidence, then rule" one page and one act instead of three.
//
// The kind is not in the route. The id names it — the server refuses an id
// whose prefix it cannot open — so every link to a record anywhere in the app
// is /r/ plus the id, and a reader who follows one never lands on a page that
// is right about the id and wrong about the kind.
export default function RecordPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [record, setRecord] = useState<RecordPeel | null>(null);
  const [error, setError] = useState<string | null>(null);
  // What the operator's last act did, announced rather than drawn: a vote that
  // changes an arrow's appearance says nothing to a screen reader, and the
  // ruling it sits beside is permanent.
  const [announcement, setAnnouncement] = useState("");
  // The stance the store confirmed on this page's own vote, held until a read
  // carries it. It lives here because two parts of the page need it: the
  // arrows in the post header, which he pressed, and depth 4, which is where
  // his position is read beside Babel's.
  const [recorded, setRecorded] = useState<OperatorReception | undefined>(undefined);
  // How many rulings this page has recorded. A ruling appends to the thread,
  // so bumping this is how the thread learns to read itself again.
  const [ruled, setRuled] = useState(0);
  // The arrows themselves, so the page's `a`/`d`/`u` press the real control.
  const vote = useRef<HTMLDivElement | null>(null);

  const load = useCallback(() => {
    let live = true;
    setError(null);
    getRecord(id)
      .then((value) => {
        if (live) setRecord(value);
      })
      .catch((reason) => {
        if (live) setError(errorMessage(reason));
      });
    return () => {
      live = false;
    };
  }, [id]);

  useEffect(() => {
    setRecord(null);
    // The confirmed stance belongs to the record it was recorded on, so
    // following a link to another one starts from that record's own reception
    // rather than from the last vote the reader cast.
    setRecorded(undefined);
    return load();
  }, [load]);

  // A record that cannot be read is a different page from a record that
  // cannot be rendered: this one names the server's own sentence, because
  // "no record pro_… in this deployment" is the whole answer and rephrasing
  // it would only hide which of the two happened.
  if (error && !record) {
    return (
      <section className="page">
        <div className="surface state-note error-state">
          <strong>This record could not be read.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!record) {
    return (
      <section className="page">
        <div className="surface state-note">
          <span className="spinner" /> Reading the record…
        </div>
      </section>
    );
  }

  return (
    <section className="page">
      <RecordHeading
        record={record}
        recorded={recorded}
        voteRef={vote}
        onVoted={(stance) => {
          setRecorded(stance);
          setAnnouncement(`Your vote is recorded: ${stance.stance}. It decides nothing.`);
        }}
      />

      {/* The terms the record is being shown on, when they are not the usual
          ones — a record resolved through the shared catalog while the catalog
          itself could not be consulted. Babel's own sentence, at the top,
          because it qualifies everything below it. */}
      {record.notice && (
        <div className="surface state-note scope-notice" role="status">
          <span>{record.notice}</span>
        </div>
      )}

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {/* A fault while rendering one record must not blank the app. The
          boundary is keyed by id so following a link to another record clears
          a fault rather than stranding the reader on a dead page, and the
          heading and the navigation survive the body that failed. */}
      <RenderBoundary key={id}>
        <RecordPeels
          record={record}
          recorded={recorded}
          voteRef={vote}
          onActed={(message) => {
            setAnnouncement(message);
            setRuled((current) => current + 1);
            // The record is re-read because an act changed it: a ruling moves
            // the standing and appends to the history, and a page still
            // showing the old standing beside the button that changed it is
            // the one thing worse than a slow reload.
            load();
          }}
        />
        {/* The conversation, under the five depths and reachable as #comments:
            §8.7 puts the thread under the post rather than beside it. */}
        <RecordThread id={id} reload={ruled} onPosted={setAnnouncement} />
      </RenderBoundary>
    </section>
  );
}
