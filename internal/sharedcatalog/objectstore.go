package sharedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/envelope"
)

// ObjectStore is the narrow injected port Babel writes sealed Phase B payloads
// through, in the same shape as the restic wrapper Phase A injects: this
// package invokes a contract, never a provider SDK. The deployment supplies a
// Cellar-backed implementation and tests supply an in-memory one, and neither
// choice reaches the commit protocol.
//
// It is two methods because the protocol needs exactly two. SPEC.md 6.5 makes
// an object's durability the precondition for the row that names it, so a write
// must be followed by a read that proves it - and there is no delete, because
// remote analysis material is never deleted by normal processing (SPEC.md 9).
// A store implementation may be shared between instances and must be safe for
// concurrent use.
type ObjectStore interface {
	// Put writes data under key. Overwriting an existing key with identical
	// content must succeed; keys are content-addressed (see objectKey), so
	// that is the only overwrite the protocol can produce.
	Put(ctx context.Context, key string, data []byte) error
	// Get returns exactly the bytes stored under key, or an error if the key
	// is absent or unreadable.
	Get(ctx context.Context, key string) ([]byte, error)
}

// ErrObjectVerification reports that an object read back from the store is not
// the object that was written.
//
// It is a distinct error because the response differs from a write failure: a
// failed write may simply be retried, while a store that acknowledges a write
// and returns different bytes is corrupting or reordering data, and continuing
// to reference it would commit a row that points at something Babel never
// produced.
var ErrObjectVerification = errors.New("sealed object read back does not match what was written")

// payloadField names the sealed field inside an analysis record's associated
// data. A record's whole payload is one envelope, so the field is constant, but
// naming it keeps the AAD three-part shape that envelope.AAD documents and
// leaves room for a future record that seals more than one field.
const payloadField = "payload"

// objectKey derives an analysis record's object-store key from its identity and
// the digest of the sealed bytes.
//
// Content addressing is what makes a retry safe. Sealing is randomized, so the
// second attempt at a record produces different ciphertext; if the key ignored
// the digest, that attempt would overwrite an object a committed row may
// already name, and the row's own digest would stop matching. With the digest
// in the key, a retry writes a new object beside the old one. The unreferenced
// object is harmless - Babel never deletes remote objects, and an orphan costs
// storage where a rewritten object would cost correctness.
//
// The record id is safe to embed: it is an opaque client-generated identifier
// that PostgreSQL already holds in plaintext, and validRecordID has already
// rejected any shape that could escape a path-like key namespace.
func objectKey(recordID, digest string) string {
	return "analysis/" + recordID + "/" + digest
}

// sealRecord seals a plaintext payload and returns the exact bytes to store.
//
// Sealing happens here, at the boundary, rather than in the caller: this
// package exposes no way to hand it bytes that are already-or-maybe-not
// encrypted, so "payloads are sealed before they leave the machine" is a
// property of the API rather than a rule a caller must remember. The associated
// data binds the envelope to the record's kind and global id, so an object
// moved to a different row fails to open (SPEC.md 9).
func sealRecord(ring *envelope.Keyring, rec StagedRecord) (object []byte, env envelope.Envelope, err error) {
	env, err = ring.Seal(recordAAD(rec.RecordID, rec.Kind), rec.Payload)
	if err != nil {
		return nil, envelope.Envelope{}, fmt.Errorf("seal record %s: %w", rec.RecordID, err)
	}
	object, err = json.Marshal(env)
	if err != nil {
		return nil, envelope.Envelope{}, fmt.Errorf("encode sealed record %s: %w", rec.RecordID, err)
	}
	return object, env, nil
}

// recordAAD is the associated data every analysis record is sealed under. It is
// one function so the writer and the reader cannot drift: a mismatch would not
// be a wrong answer but an authentication failure at read time, long after the
// key was chosen.
func recordAAD(recordID string, kind RecordKind) envelope.AAD {
	return envelope.AAD{Type: string(kind), ID: recordID, Field: payloadField}
}

// OpenRecord fetches a committed record's sealed object, verifies it against
// the digest the catalog recorded, and decrypts it.
//
// This is the read half of global durability (SPEC.md 4.7): a second instance
// holding only the catalog credential and the keyring can browse a run another
// machine committed and recover its content, without that machine being
// reachable. The digest check runs before decryption so a swapped or truncated
// object is reported as a storage fault rather than surfacing as an opaque
// authentication failure.
func OpenRecord(ctx context.Context, store ObjectStore, ring *envelope.Keyring, row AnalysisRecordRow) ([]byte, error) {
	if store == nil {
		return nil, errors.New("open analysis record: object store is required")
	}
	if ring == nil {
		return nil, errors.New("open analysis record: keyring is required")
	}
	object, err := store.Get(ctx, row.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("read sealed object for record %s: %w", row.RecordID, err)
	}
	if digestOf(object) != row.ObjectDigest {
		return nil, fmt.Errorf("record %s: %w", row.RecordID, ErrObjectVerification)
	}
	var env envelope.Envelope
	if err := json.Unmarshal(object, &env); err != nil {
		return nil, fmt.Errorf("decode sealed object for record %s: %w", row.RecordID, err)
	}
	plaintext, err := ring.Open(recordAAD(row.RecordID, row.Kind), env)
	if err != nil {
		return nil, fmt.Errorf("open record %s: %w", row.RecordID, err)
	}
	return plaintext, nil
}

// digestOf is the digest the catalog stores and every read verifies. It is
// taken over the sealed bytes, never the plaintext: a plaintext digest would be
// a deterministic function of the payload, and SPEC.md 9 forbids putting one of
// those in PostgreSQL because it lets an observer match equal payloads across
// rows without holding a key.
func digestOf(object []byte) string {
	sum := sha256.Sum256(object)
	return hex.EncodeToString(sum[:])
}
