package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/atyrode/babel/internal/config"
)

// Store is the object-store contract Babel's Phase B commit protocol invokes.
//
// It is two methods because the protocol needs exactly two. SPEC.md 6.5 makes
// an object's durability the precondition for the row that names it, so a write
// must be followed by a read that proves it - and there is no delete, because
// remote analysis material is never deleted by normal processing (SPEC.md 9).
//
// It is declared here rather than imported, and it is structurally identical to
// the shared catalog's ObjectStore port, which it satisfies without an import
// edge in either direction: Go interfaces are structural, so the same
// implementation answers both callers while this package stays strictly lower
// in the dependency order than either of them. That matters because the
// publisher and the fleet reader both need this code, and the catalog's own
// port comment states the rule this package would otherwise break - the catalog
// invokes a contract and never a provider SDK, so the signing client cannot
// live there.
//
// An implementation may be shared between callers and must be safe for
// concurrent use.
type Store interface {
	// Put writes data under key. Overwriting an existing key with
	// identical content must succeed; keys are content-addressed, so that
	// is the only overwrite the protocol can produce.
	Put(ctx context.Context, key string, data []byte) error
	// Get returns exactly the bytes stored under key, or an error wrapping
	// ErrObjectAbsent if the store does not hold it.
	Get(ctx context.Context, key string) ([]byte, error)
}

// dirPerm is the mode every Babel-owned directory carries, matching
// frontier.Open's durable state directory. Sealed objects are ciphertext, but a
// directory listing still names record ids in plaintext, and no other user on
// the machine has business reading Babel's state.
const dirPerm = 0o700

// stagingPattern names the temporary file a local Put writes before it renames
// it into place. The leading dot is load-bearing twice over: it keeps staging
// files out of a casual listing, and it is a shape a real object key cannot
// have, because every key segment comes from a validated record id (which must
// begin with an alphanumeric) or from a hex digest. A staging file left behind
// by a crash can therefore never be mistaken for an object.
const stagingPattern = ".staging-*"

// ErrObjectAbsent reports that the store holds no object under the requested
// key.
//
// It is exported and distinguishable because the commit protocol's read-back
// asks a question no other error answers. SPEC.md 6.5 makes an object's
// durability the precondition for the row that names it, so a publisher writes,
// then reads back to prove the write; "not there yet" is a state that retrying
// resolves, while a refused, unreachable or misconfigured endpoint is a fault
// that retrying will not. A caller that could not tell them apart would either
// hammer a broken endpoint forever or treat a missing object as durable.
//
// Both implementations in this package return it, so a caller never has to know
// which store it was handed.
var ErrObjectAbsent = errors.New("object store holds no object under that key")

// DirStore is a local-directory object store: one file per key, under a root
// this deployment owns.
//
// It exists for the same reason Phase A proves local-path repositories before
// Cellar (SPEC.md 14). A development or recovery deployment configured with a
// filesystem repository still needs somewhere for sealed Phase B objects to go,
// and "the same commit protocol, a different store" is the entire point of
// injecting the port. This is not a cache or a mirror of a remote store: where
// it is the store, it is the only copy, which is why Put goes to the trouble
// below rather than calling os.WriteFile.
//
// It is safe for concurrent use and holds no lock. Keys are content addressed,
// so two writers racing on one key are writing identical bytes, and the write
// lands by rename, which is atomic within a filesystem: whichever writer loses
// the race replaces the winner's file with a byte-identical one, and every
// concurrent reader sees a complete object either way.
type DirStore struct {
	root string
}

// NewDirStore opens the object store rooted at root, creating the directory if
// it is not there.
//
// The root is created eagerly rather than on first Put so that a
// misconfiguration - an unwritable parent, a path that is already a file - is
// reported when the deployment is opened instead of in the middle of a commit.
func NewDirStore(root string) (*DirStore, error) {
	if root == "" {
		return nil, errors.New("local object store root is required")
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("create local object store root: %w", withoutPath(err))
	}
	return &DirStore{root: root}, nil
}

// Put writes data under key, atomically.
//
// The bytes go to a staging file in the destination directory, are forced to
// the device, and only then acquire their name by rename. That sequence is not
// ceremony: the commit protocol reads an object back to prove durability before
// any PostgreSQL row names it, so a partially written file that answered a read
// would let a row point at a truncated object. Rename within one directory is
// atomic, so the key either does not exist or names the whole object, and never
// names half of one.
func (s *DirStore) Put(ctx context.Context, key string, data []byte) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	// The filesystem calls below are not cancellable, so the context is
	// honoured where it can be: at entry, before any of them starts.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create directory for object %s: %w", key, withoutPath(err))
	}
	// os.CreateTemp creates the file with mode 0o600 before umask, which is
	// the mode the object needs; setting it again would only add a syscall
	// and a second place for the number to be wrong.
	staging, err := os.CreateTemp(dir, stagingPattern)
	if err != nil {
		return fmt.Errorf("stage object %s: %w", key, withoutPath(err))
	}
	// A staging file that survives a failure is removed here. After a
	// successful rename its name is gone and the remove is a no-op.
	defer os.Remove(staging.Name())
	if err := writeAndSync(staging, data); err != nil {
		return fmt.Errorf("stage object %s: %w", key, withoutPath(err))
	}
	if err := os.Rename(staging.Name(), path); err != nil {
		return fmt.Errorf("commit object %s: %w", key, withoutPath(err))
	}
	return nil
}

// Get returns the bytes stored under key, or an error wrapping ErrObjectAbsent
// if the store does not hold it.
func (s *DirStore) Get(ctx context.Context, key string) ([]byte, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("get object %s: %w", key, ErrObjectAbsent)
		}
		return nil, fmt.Errorf("get object %s: %w", key, withoutPath(err))
	}
	return data, nil
}

// objectPath resolves a key to a path inside the store, refusing anything that
// is not a key.
func (s *DirStore) objectPath(key string) (string, error) {
	if err := validObjectKey(key); err != nil {
		return "", err
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// writeAndSync writes the whole object and forces it to the device before
// closing the file.
//
// Sync is what makes the rename that follows meaningful. Without it the name
// could become visible while the bytes were still only in the page cache, and
// the read-back the commit protocol performs would be answered out of that same
// cache - proving nothing about a crash that has not happened yet.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// withoutPath reduces a filesystem error to its cause, dropping the path the
// operating system repeated into it.
//
// Every message in this file already names the key, which is the identifier the
// caller, the catalog row and the journal all use. The absolute path adds the
// operator's directory layout to a diagnostic that is printed on a terminal
// beside remote errors, and no reader of it needs the store root restated.
func withoutPath(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	// os.Rename reports *os.LinkError, which repeats two paths rather than
	// one.
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}

// validObjectKey bounds what may be used as an object key: a clean relative
// path of non-empty segments, none of which is "." or "..".
//
// The shared catalog already forbids any record id that could escape the key
// namespace, and every key the protocol produces is built from one of those
// plus a hex digest. This check is still here, and it runs before the key is
// joined to anything, because this is the boundary that would actually obey a
// traversal: a filesystem-backed store handed "analysis/../../etc/x" writes to
// /etc/x, and an S3 endpoint handed a "." or ".." segment may normalize the
// path it received and then disagree with the signature computed over the path
// that was sent. Validating in the store rather than trusting the caller is
// what keeps a future caller - a repair tool, a migration, a fixture - from
// being the one place the invariant was forgotten.
func validObjectKey(key string) error {
	if key == "" {
		return errors.New("object key is empty")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("object key %q is absolute; keys are relative to the store root", key)
	}
	for _, segment := range strings.Split(key, "/") {
		switch segment {
		case "":
			return fmt.Errorf("object key %q has an empty segment", key)
		case ".", "..":
			return fmt.Errorf("object key %q has a %q segment", key, segment)
		}
	}
	return nil
}

// locationKind names which of the two Phase B object stores a repository
// locator implies.
//
// There are two because SPEC.md 710 decision 36 states Babel's provider
// compatibility as S3 plus PostgreSQL: a third kind would be a new
// compatibility claim, not a new case. The zero value is deliberately not a
// kind, so an unset field cannot pass for a decision.
type locationKind int

const (
	locationDir locationKind = iota + 1
	locationS3
)

// objectLocation is where this deployment's sealed Phase B objects live.
type objectLocation struct {
	kind locationKind

	// dir is the filesystem root, set when kind is locationDir.
	dir string

	// endpoint, bucket and prefix describe the S3 target, set when kind is
	// locationS3. endpoint is scheme://host with no trailing slash, and
	// prefix is an object key prefix ending in "/".
	endpoint string
	bucket   string
	prefix   string
}

// analysisSuffix is what makes the Phase B location a sibling of the restic
// repository rather than a child of it. It is appended to the locator's last
// component, so "babel/v1" becomes "babel/v1-analysis" - a name restic's tree
// cannot contain, because restic's tree is everything under "babel/v1/".
const analysisSuffix = "-analysis"

// defaultKeyPrefix is the sibling for a repository that sits at a bucket root,
// where there is no prefix to append a suffix to. restic's own top-level names
// there are config, keys, index, data, snapshots and locks, so Babel takes a
// name of its own instead.
const defaultKeyPrefix = "babel" + analysisSuffix + "/"

// phaseBLocation derives the Phase B object location from the restic repository
// locator, returning the derived location and which kind of store it is.
//
// It is derived rather than configured because storage.json is frozen at
// config_schema 2 (SPEC.md 741) and a Phase B object root is not one of its
// fields. Deriving one costs the operator nothing to maintain: the same
// credential, the same bucket, the same account, provisioned once, and no
// second place where a deployment can be half-configured.
//
// The derived location is deliberately BESIDE the restic repository and never
// inside it. restic owns its repository layout - config, keys, index, data,
// snapshots, locks - and a foreign key inside that tree is a hazard both ways:
// restic is free to give a future layout a name Babel picked, and a Babel
// object under restic's prefix is something `restic check` sees and Babel
// cannot explain. A sibling is provably disjoint rather than conventionally
// disjoint. For `s3:https://host/bucket/babel/v1` the objects land under the
// key prefix `babel/v1-analysis/`, which is not a prefix of `babel/v1/` and
// cannot become one; for the local path `/srv/babel/repo` they land in
// `/srv/babel/repo-analysis`.
func phaseBLocation(repository string) (objectLocation, error) {
	if repository == "" {
		return objectLocation{}, errors.New("storage configuration names no repository, so no Phase B object location can be derived")
	}
	scheme, rest, hasScheme := splitLocatorScheme(repository)
	if !hasScheme {
		if !filepath.IsAbs(repository) {
			return objectLocation{}, fmt.Errorf("repository %q is a relative path; a Phase B object root derived from it would depend on the process working directory rather than on configuration", repository)
		}
		return objectLocation{kind: locationDir, dir: filepath.Clean(repository) + analysisSuffix}, nil
	}
	if scheme != "s3" {
		// Named, bounded, and no silent fallback: a deployment whose
		// repository lives on a backend Babel cannot address for Phase B
		// objects must hear so when it is opened, rather than discover
		// later that its analysis went somewhere else.
		return objectLocation{}, fmt.Errorf(
			"repository scheme %q cannot hold Phase B objects: Babel's provider compatibility is S3 plus PostgreSQL (SPEC.md 710 decision 36), and Phase B object storage implements a local path and S3",
			scheme+":")
	}
	return s3Location(rest)
}

// splitLocatorScheme separates a restic locator's leading "scheme:" from the
// rest, reporting whether there was one. restic reads a locator the same way,
// and a bare path is the local case; a colon that appears after a path
// separator belongs to a filename rather than to a scheme.
func splitLocatorScheme(repository string) (scheme, rest string, ok bool) {
	colon := strings.IndexByte(repository, ':')
	if colon <= 0 {
		return "", repository, false
	}
	if slash := strings.IndexByte(repository, '/'); slash >= 0 && slash < colon {
		return "", repository, false
	}
	// A scheme is a letter followed by letters, digits, "+", "-" or "."
	// (RFC 3986). The digits are not decoration: restic's own backends are
	// named s3 and b2, so a letters-only rule would read the operator's
	// real locator as a path with a colon in it.
	scheme = repository[:colon]
	for i := range len(scheme) {
		c := scheme[i]
		letter := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
		if i == 0 {
			if !letter {
				return "", repository, false
			}
			continue
		}
		if !letter && !('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.') {
			return "", repository, false
		}
	}
	return scheme, repository[colon+1:], true
}

// s3Location parses the S3 locator forms restic accepts against a path-style
// endpoint: `https://HOST/BUCKET[/PREFIX...]`, the http variant, and the
// scheme-less `HOST/BUCKET[/PREFIX...]`, which means https.
//
// Path style - the bucket as the first path segment rather than a label in the
// hostname - is what restic uses against Cellar and what docs/runbook.md's
// `s3:https://<cellar-host>/<bucket>/babel/v1` records, so it is what this
// package signs. Accepting the scheme-less spelling matters because it is the
// short form an operator writes by hand, and defaulting it to https rather than
// http means a locator missing its scheme cannot quietly downgrade transport.
func s3Location(rest string) (objectLocation, error) {
	scheme := "https"
	switch {
	case strings.HasPrefix(rest, "https://"):
		rest = strings.TrimPrefix(rest, "https://")
	case strings.HasPrefix(rest, "http://"):
		scheme, rest = "http", strings.TrimPrefix(rest, "http://")
	}
	host, path, _ := strings.Cut(rest, "/")
	if host == "" {
		return objectLocation{}, errors.New("s3 repository locator names no host; the expected form is s3:https://HOST/BUCKET[/PREFIX]")
	}
	bucket, prefix, _ := strings.Cut(strings.Trim(path, "/"), "/")
	if bucket == "" {
		return objectLocation{}, errors.New("s3 repository locator names no bucket; the expected form is s3:https://HOST/BUCKET[/PREFIX]")
	}
	// A sibling prefix inside the operator-provisioned bucket, never a
	// subtree of restic's: see phaseBLocation. This is the whole reason a
	// frozen schema needs no new field to say where Phase B objects go.
	keyPrefix := defaultKeyPrefix
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		keyPrefix = prefix + analysisSuffix + "/"
	}
	return objectLocation{
		kind:     locationS3,
		endpoint: scheme + "://" + host,
		bucket:   bucket,
		prefix:   keyPrefix,
	}, nil
}

// Open builds the Phase B object store this deployment's storage configuration
// names.
//
// It reads exactly two things: the restic repository locator, which says where
// the objects go (phaseBLocation), and the object-store credential, which says
// how to get at them. It deliberately does not read cfg.Mode. Whether shared
// publication is configured at all is the caller's question - local mode has no
// catalog to commit rows to, so the decision belongs where that catalog is
// opened - and answering it here as well would put one rule in two places and
// let them disagree.
func Open(cfg config.Config) (Store, error) {
	location, err := phaseBLocation(cfg.Repository)
	if err != nil {
		return nil, err
	}
	if location.kind == locationDir {
		store, err := NewDirStore(location.dir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	// The messages below name which half of the credential is missing and
	// never quote either half. config's own validation already refuses a
	// document carrying one half alone, so reaching one of these means the
	// credential was dropped between the document and here - and the fix is
	// in the document either way.
	credential := cfg.RepositoryStore
	if credential == nil {
		return nil, errors.New("an s3 repository needs repository_store credentials to hold Phase B objects, and the storage configuration carries none")
	}
	if credential.AccessKeyID == "" {
		return nil, errors.New("storage configuration repository_store.access_key_id is empty")
	}
	if credential.SecretAccessKey == "" {
		return nil, errors.New("storage configuration repository_store.secret_access_key is empty")
	}
	// DefaultRegion because the frozen schema carries no region field; see
	// the constant for why signing Phase B objects under the same scope
	// restic's own objects use is a default rather than a guess.
	store, err := NewS3Store(location.endpoint, location.bucket, location.prefix, DefaultRegion,
		credential.AccessKeyID, credential.SecretAccessKey, nil)
	if err != nil {
		return nil, err
	}
	return store, nil
}
