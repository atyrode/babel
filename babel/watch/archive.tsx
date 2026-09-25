import { Cluster, Stack } from "@manifold/ui";
import { ARCHIVE_NOTE, figure, sessionsClause, type ArchiveLabels } from "./api.ts";

/*
  THE ARCHIVE LABELS NO MACHINE ANSWERS FOR (#453).

  Every session Babel reads comes out of the fleet archive, filed under the restic host label
  its snapshot was taken with. A label is a machine's name as the collector spelled it, not a
  hub machine id, and it becomes one only where the owner recorded the mapping. An unmapped
  label is how the operator learns that a machine collects under a name the hub does not know,
  and the session count is how much of the corpus that is.

  IT IS ONE LINE AND IT IS NOT A WARNING. Those sessions are still selected and prepared, and a
  retired machine's label stays unmapped on purpose, so the section wears no alarm border. It
  renders nothing when every label is mapped: a heading reporting "all mapped" on every poll is
  the absence of information with a frame round it. A failed pulse read is said once, by the
  last cycle's section, which reads the same door.
*/

export interface ArchiveProps {
  /** The pulse's archive half; null until the first read answered. */
  readonly archive: ArchiveLabels | null;
}

export function Archive({ archive }: ArchiveProps) {
  if (archive === null || (archive.unmapped.length === 0 && archive.omitted === 0)) return null;
  return (
    <Stack
      gap="var(--babel-space-2)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__archive"
    >
      <h2 className="plugin-atyrode_babel_watch__title">Archive</h2>
      <Cluster gap="var(--babel-space-4)">
        {archive.unmapped.map((label) => (
          <span key={label.label} className="plugin-atyrode_babel_watch__archive-label">
            <span className="plugin-atyrode_babel_watch__mono">{label.label}</span>{" "}
            <span className="plugin-atyrode_babel_watch__muted">
              {sessionsClause(label.sessions)}
            </span>
          </span>
        ))}
        {archive.omitted === 0 ? null : (
          <span className="plugin-atyrode_babel_watch__muted">
            and {figure(archive.omitted)} more {archive.omitted === 1 ? "label" : "labels"}
          </span>
        )}
      </Cluster>
      <p className="plugin-atyrode_babel_watch__muted">{ARCHIVE_NOTE}</p>
    </Stack>
  );
}
