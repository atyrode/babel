package restic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// maxJSONLine bounds one line of restic's --json stream. Status lines name
// the files currently being read, so they can be long; anything past this
// is a protocol violation rather than a message worth parsing.
const maxJSONLine = 1 << 20

// newline terminates each line mirrored into an error tail.
var newline = []byte{'\n'}

// ErrRepoMissing reports that the repository does not exist yet. It is a
// distinct error because creating one is an explicit operator act, never a
// side effect of backing up: see Init.
var ErrRepoMissing = errors.New("repository does not exist")

// Require reports whether the repository exists and the password opens it,
// returning ErrRepoMissing when it does not.
//
// Every command that reads or writes an existing repository calls this rather
// than creating one, so a mistyped locator fails loudly instead of quietly
// growing a second, empty archive somewhere the operator is not looking.
func (r *Repo) Require(ctx context.Context) error {
	err := r.probe(ctx)
	switch {
	case err == nil:
		return nil
	case isMissingRepo(err):
		return ErrRepoMissing
	}
	return err
}

// Init creates the repository, and reports whether this call created it.
//
// It is NOT safe to run concurrently with another Init against the same absent
// repository, and nothing in Babel calls it on an unattended path for that
// reason. restic generates a master key per init and writes the key before the
// config; two inits racing on an empty repository both succeed, leaving two
// valid keys and one config. restic then picks a key by iteration, and when it
// picks the wrong one it fails with "config or key <id> is damaged: ciphertext
// verification failed" - a repository that has to be repaired by hand.
// Measured against restic 0.19.1: 10 of 10 races left two keys, and 7 of 10
// subsequent backups failed (2026-08-29).
//
// So initialization is an explicit one-time bootstrap step, and an existing
// repository is reported rather than re-created.
func (r *Repo) Init(ctx context.Context) (created bool, err error) {
	if err := r.Require(ctx); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrRepoMissing) {
		return false, err
	}

	if _, err := r.run(ctx, "init", "init"); err != nil {
		// A concurrent init may have won. The config is the arbiter, but a
		// repository that came into existence this way is not trustworthy, so
		// this reports the failure rather than calling it success.
		return false, err
	}
	return true, nil
}

// probe reads the repository config, the cheapest proof that the repository
// exists and the password file opens it.
func (r *Repo) probe(ctx context.Context) error {
	_, err := r.run(ctx, "cat config", "cat", "config")
	return err
}

// isMissingRepo reports whether err says the repository does not exist:
// restic exits 10 for that, and older versions only say so in prose.
func isMissingRepo(err error) bool {
	if exitCode(err) == exitNoSuchRepo {
		return true
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		lower := strings.ToLower(exitErr.Stderr)
		return strings.Contains(lower, "is there a repository") ||
			strings.Contains(lower, "repository does not exist")
	}
	return false
}

// BackupSummary is the outcome of one backup, taken from restic's summary
// message. DataAdded is the repository growth in bytes before compression:
// it is far below TotalBytesProcessed whenever deduplication finds existing
// chunks, which is the normal case for append-only session files.
type BackupSummary struct {
	SnapshotID          string
	FilesNew            int
	FilesChanged        int
	FilesUnmodified     int
	DataAdded           int64
	TotalFilesProcessed int
	TotalBytesProcessed int64
}

// backupMessage is one line of restic's `backup --json` stream. The union
// is flat: unrelated fields stay zero for a given message_type.
type backupMessage struct {
	MessageType string `json:"message_type"`

	// summary
	FilesNew            int    `json:"files_new"`
	FilesChanged        int    `json:"files_changed"`
	FilesUnmodified     int    `json:"files_unmodified"`
	DataAdded           int64  `json:"data_added"`
	TotalFilesProcessed int    `json:"total_files_processed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	SnapshotID          string `json:"snapshot_id"`

	// error / exit_error
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	During  string `json:"during"`
	Item    string `json:"item"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Backup snapshots paths under the given host and tags. Paths are recorded
// as given, so callers should pass absolute paths.
//
// Per-file read failures are non-fatal in restic: it finishes the snapshot
// and exits 3. Backup mirrors that — it returns the summary of the snapshot
// that was created together with an error matching ErrIncomplete, so a
// caller may accept the partial snapshot or retry. Each unreadable item is
// reported as one line on Config.Diagnostics (paths and restic's message
// only, never file content).
func (r *Repo) Backup(ctx context.Context, paths []string, host string, tags []string) (*BackupSummary, error) {
	if len(paths) == 0 {
		return nil, errors.New("restic backup: no paths")
	}

	args := []string{"backup", "--json"}
	if host != "" {
		args = append(args, "--host", host)
	}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}
	// "--" keeps a path that starts with "-" from being read as a flag.
	args = append(args, "--")
	args = append(args, paths...)

	cmd, err := r.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("restic backup: stdout pipe: %w", err)
	}
	// restic splits the protocol across both streams: the summary message
	// arrives on stdout while per-item errors and the final exit_error land
	// on stderr, so both are parsed and stderr is also kept as an error tail.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("restic backup: stderr pipe: %w", err)
	}
	stderrTail := &tailBuffer{limit: stderrTailLimit}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("restic backup: starting: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.consumeBackupStream(stderrPipe, stderrTail)
	}()
	summary, scanErr := r.consumeBackupStream(stdout, nil)
	// Both pipes must reach EOF before Wait closes them.
	wg.Wait()

	waitErr := cmd.Wait()
	if waitErr != nil {
		wrapped := wrapExit(ctx, "backup", waitErr, stderrTail)
		if exitCode(wrapped) == exitIncomplete {
			return summary, fmt.Errorf("%w: %w", ErrIncomplete, wrapped)
		}
		return nil, wrapped
	}
	if scanErr != nil {
		return nil, fmt.Errorf("restic backup: reading json stream: %w", scanErr)
	}
	if summary == nil {
		return nil, ErrNoSummary
	}
	return summary, nil
}

// consumeBackupStream parses one ndjson stream of restic's backup protocol,
// returning the summary message if this stream carried it. Every line is
// mirrored to tail when tail is non-nil, so a fatal non-JSON message still
// reaches the caller's error. Unparseable lines are skipped: a torn or
// unknown message must not fail a backup restic considers successful. The
// stream is always drained, so the child never blocks on a full pipe.
func (r *Repo) consumeBackupStream(stream io.Reader, tail io.Writer) (*BackupSummary, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64<<10), maxJSONLine)

	var summary *BackupSummary
	for scanner.Scan() {
		line := scanner.Bytes()
		if tail != nil {
			_, _ = tail.Write(line)
			_, _ = tail.Write(newline)
		}
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg backupMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch msg.MessageType {
		case "summary":
			summary = &BackupSummary{
				SnapshotID:          msg.SnapshotID,
				FilesNew:            msg.FilesNew,
				FilesChanged:        msg.FilesChanged,
				FilesUnmodified:     msg.FilesUnmodified,
				DataAdded:           msg.DataAdded,
				TotalFilesProcessed: msg.TotalFilesProcessed,
				TotalBytesProcessed: msg.TotalBytesProcessed,
			}
		case "error":
			r.diagnose("backup: %s: %s", itemOrDuring(msg), msg.Error.Message)
		case "exit_error":
			r.diagnose("backup: exit %d: %s", msg.Code, msg.Message)
		}
	}
	err := scanner.Err()
	_, _ = io.Copy(io.Discard, stream)
	return summary, err
}

// itemOrDuring names what an error message was about.
func itemOrDuring(msg backupMessage) string {
	if msg.Item != "" {
		return msg.Item
	}
	if msg.During != "" {
		return "during " + msg.During
	}
	return "unknown item"
}

// diagnose reports one non-fatal restic warning to the configured sink.
func (r *Repo) diagnose(format string, args ...any) {
	if r.cfg.Diagnostics == nil {
		return
	}
	r.diagMu.Lock()
	defer r.diagMu.Unlock()
	fmt.Fprintf(r.cfg.Diagnostics, "restic "+format+"\n", args...)
}

// Snapshot is one snapshot as listed by the repository.
type Snapshot struct {
	ID      string
	ShortID string
	Time    time.Time
	Host    string
	Tags    []string
	Paths   []string

	// Summary carries the counts restic recorded when the snapshot was made.
	// It is nil when the snapshot record has none: the field is optional in
	// restic's output, and a snapshot lacking it has no counts to report rather
	// than counts of zero. Callers must treat nil as "unknown", never as zero.
	Summary *SnapshotSummary
}

// SnapshotSummary is the subset of restic's stored backup summary Babel records.
type SnapshotSummary struct {
	FilesNew            int64
	FilesChanged        int64
	FilesUnmodified     int64
	DataAdded           int64
	TotalFilesProcessed int64
	TotalBytesProcessed int64
}

// snapshotJSON is one element of `restic snapshots --json`.
type snapshotJSON struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Paths    []string  `json:"paths"`
	Summary  *struct {
		FilesNew            int64 `json:"files_new"`
		FilesChanged        int64 `json:"files_changed"`
		FilesUnmodified     int64 `json:"files_unmodified"`
		DataAdded           int64 `json:"data_added"`
		TotalFilesProcessed int64 `json:"total_files_processed"`
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

// shortIDLen is the length of restic's abbreviated snapshot identifier.
const shortIDLen = 8

// Snapshots lists the repository's snapshots, newest last (restic's own
// order). An empty repository yields an empty slice, not an error.
func (r *Repo) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := r.run(ctx, "snapshots", "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var raw []snapshotJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("restic snapshots: parsing json: %w", err)
	}
	return snapshotsFromJSON(raw), nil
}

// snapshotsFromJSON converts restic's records, kept separate from the command
// so optional-field handling can be tested against fixtures without a binary.
func snapshotsFromJSON(raw []snapshotJSON) []Snapshot {
	snaps := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		short := s.ShortID
		if short == "" && len(s.ID) >= shortIDLen {
			short = s.ID[:shortIDLen]
		}
		snap := Snapshot{
			ID:      s.ID,
			ShortID: short,
			Time:    s.Time,
			Host:    s.Hostname,
			Tags:    s.Tags,
			Paths:   s.Paths,
		}
		// Preserve absence: a snapshot without a stored summary reports nil
		// rather than a row of zeros, so a caller cannot mistake "unknown" for
		// "nothing was backed up".
		if s.Summary != nil {
			snap.Summary = &SnapshotSummary{
				FilesNew:            s.Summary.FilesNew,
				FilesChanged:        s.Summary.FilesChanged,
				FilesUnmodified:     s.Summary.FilesUnmodified,
				DataAdded:           s.Summary.DataAdded,
				TotalFilesProcessed: s.Summary.TotalFilesProcessed,
				TotalBytesProcessed: s.Summary.TotalBytesProcessed,
			}
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

// Check verifies repository integrity. With readData false it validates
// structure, indexes and every metadata blob; with readData true it also
// re-reads and re-hashes all pack files, which costs one full read of the
// repository. A returned error carries a bounded tail of restic's report.
func (r *Repo) Check(ctx context.Context, readData bool) error {
	args := []string{"check"}
	op := "check"
	if readData {
		args = append(args, "--read-data")
		op = "check --read-data"
	}
	_, err := r.run(ctx, op, args...)
	return err
}

// Dump streams the contents of path inside snapshotID to w. path is the
// path as recorded in the snapshot (absolute, as passed to Backup); a
// directory is written as a tar archive by restic.
func (r *Repo) Dump(ctx context.Context, snapshotID, path string, w io.Writer) error {
	if snapshotID == "" {
		return errors.New("restic dump: no snapshot id")
	}
	if path == "" {
		return errors.New("restic dump: no path")
	}
	if w == nil {
		return errors.New("restic dump: no writer")
	}
	cmd, err := r.command(ctx, "dump", snapshotID, path)
	if err != nil {
		return err
	}
	stderr := &tailBuffer{limit: stderrTailLimit}
	cmd.Stdout = w
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return wrapExit(ctx, "dump", err, stderr)
	}
	return nil
}

// Restore materializes snapshotID into target. Each include is one path
// filter as recorded in the snapshot; no includes restores everything.
// restic recreates the recorded absolute paths beneath target, so a file
// snapshotted as /a/b lands at target/a/b.
func (r *Repo) Restore(ctx context.Context, snapshotID string, includes []string, target string) error {
	if snapshotID == "" {
		return errors.New("restic restore: no snapshot id")
	}
	if target == "" {
		return errors.New("restic restore: no target")
	}
	args := []string{"restore", snapshotID, "--target", target}
	for _, include := range includes {
		args = append(args, "--include", include)
	}
	_, err := r.run(ctx, "restore", args...)
	return err
}
