package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Entry is one file or directory inside a snapshot, as `restic ls` reports it.
//
// Listing a snapshot reads only metadata: it never downloads file contents. That
// is what lets one machine enumerate another machine's archived sessions without
// materializing them, which is the basis of cross-host fetch (SPEC.md 6.2).
type Entry struct {
	// Path is absolute inside the snapshot, matching the layout of the source
	// machine that took it - so it may name a home directory that does not
	// exist here.
	Path string
	Type string
	Size int64
	Time time.Time
}

// IsFile reports whether the entry is a regular file. Directories and other
// node types carry no restorable content of their own.
func (e Entry) IsFile() bool { return e.Type == "file" }

// entryJSON is one line of `restic ls --json`. The first line describes the
// snapshot and carries struct_type "snapshot"; the rest are nodes.
type entryJSON struct {
	StructType  string    `json:"struct_type"`
	MessageType string    `json:"message_type"`
	Path        string    `json:"path"`
	Type        string    `json:"type"`
	Size        int64     `json:"size"`
	Mtime       time.Time `json:"mtime"`
}

// Ls lists a snapshot's file tree, optionally narrowed to the given paths.
//
// Output is JSON lines rather than one array, so it is scanned rather than
// unmarshalled whole: a snapshot of a large session corpus can list many
// thousands of nodes.
func (r *Repo) Ls(ctx context.Context, snapshotID string, paths ...string) ([]Entry, error) {
	if snapshotID == "" {
		return nil, fmt.Errorf("restic ls: no snapshot id")
	}
	args := append([]string{"ls", "--json", snapshotID}, paths...)
	out, err := r.run(ctx, "ls", args...)
	if err != nil {
		return nil, err
	}

	var entries []Entry
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), maxJSONLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e entryJSON
		if err := json.Unmarshal(line, &e); err != nil {
			// A line Babel cannot parse is reported rather than skipped: a
			// silently dropped node would look like a session that is not
			// archived.
			return nil, fmt.Errorf("restic ls: parsing json: %w", err)
		}
		// Skip the leading snapshot record and any progress or summary message.
		if e.StructType != "node" || e.Path == "" {
			continue
		}
		entries = append(entries, Entry{
			Path: e.Path,
			Type: e.Type,
			Size: e.Size,
			Time: e.Mtime,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("restic ls: reading output: %w", err)
	}
	return entries, nil
}
