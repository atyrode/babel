package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// The Phase B payload keys live in their own document beside storage.json, and
// this file is that document.
//
// It is separate for one hard reason and one good one. The hard reason is that
// `config_schema` 2 is frozen (SPEC.md §14): the frozen tests pin storage.json's
// exact JSON names at every level, the document has run against real Cellar and
// real managed PostgreSQL, and a new field in it is a schema change rather than
// an addition. The good reason is lifecycle. A repository locator and a database
// credential are configuration an operator edits; a payload key is key material
// whose rotation is an append that must never lose a predecessor, because every
// sealed object ever written under a retired key still needs it. Those two
// things want different documents even where a schema is not frozen.
//
// This package returns key MATERIAL and never builds an envelope. internal/envelope
// states that it holds "no key management UX, no storage integration, and no
// password-based derivation", and reading a file for it here would make that
// false from the other side. The consumer decodes, then calls
// envelope.RingFrom.

// PayloadKeysName is the payload key document's file name, held beside
// storage.json in Babel's configuration directory.
const PayloadKeysName = "payload-keys.json"

// payloadKeySchema is the version this build writes. It is independent of
// storage.json's `config_schema` on purpose: two documents that shared a version
// number would have to be revised together forever.
const payloadKeySchema = 1

// payloadKeyBytes is the key length internal/envelope's AES-256-GCM
// construction requires. It is restated here rather than imported so that
// internal/config depends on nothing: a wrong length is refused at load with a
// message about the document, which is where the operator can fix it.
const payloadKeyBytes = 32

// maxPayloadKeys bounds how many keys one document may carry.
//
// A ring grows by rotation and never shrinks, because a retired key still opens
// historical objects. The bound is generous against any plausible rotation
// schedule and exists because the document is read into memory on every command
// that publishes, and an unbounded list in a mode-0600 file is still an
// unbounded allocation.
const maxPayloadKeys = 64

// maxPayloadKeysFile bounds the document read. It is far above
// maxPayloadKeys full entries and far below a memory hazard.
const maxPayloadKeysFile = 1 << 20

// validKeyID is the shape a key id may take.
//
// A key id is stored in plaintext in the shared catalog beside every ciphertext
// (SPEC.md §9 admits it), it reaches operator-facing diagnostics, and
// internal/envelope quotes it into error messages precisely because it arrives
// from durable rows. A small boring character set is what keeps it from being a
// place to smuggle anything.
var validKeyID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ErrPayloadKeysExist reports a document that is already there.
//
// Overwriting one is refused rather than confirmed, and this is the error that
// refuses it: replacing a payload key document orphans every sealed object
// written under the keys it held. Nothing in Babel deletes a remote object, so
// those objects would remain, unreadable, forever. Rotation adds a key; it never
// replaces the file.
var ErrPayloadKeysExist = errors.New("payload key document already exists")

// PayloadKeys is the payload key document: one active key for sealing and every
// key the deployment can still open with.
//
// The split is internal/envelope's rotation model, recorded durably. Sealing
// uses one key so a compromise window is bounded and a key that has approached
// the envelope's message ceiling can be retired; opening uses all of them so
// retiring one costs nothing and leaves historical objects readable.
type PayloadKeys struct {
	KeySchema int `json:"key_schema"`
	// ActiveKeyID names the key new envelopes are sealed under. It must be one
	// of Keys: a document that names a key it does not carry would seal
	// nothing and would only be discovered at the first publication.
	ActiveKeyID string `json:"active_key_id"`
	// Keys is every key the deployment holds, active and retired. Order is not
	// significant and Load sorts it, so two machines given the same keys read
	// the same document.
	Keys []PayloadKey `json:"keys"`
}

// PayloadKey is one key: an opaque operator-assigned identifier and 32 bytes of
// standard-encoding base64.
//
// Base64 rather than hex because the document is written and read by humans and
// by dotfiles, and half the characters is half the chance of a transcription
// error in a value where one wrong character is an unopenable corpus.
type PayloadKey struct {
	KeyID string `json:"key_id"`
	Key   string `json:"key"`
}

// PayloadKeysPath returns the location of the payload key document.
func PayloadKeysPath() string {
	path, _ := payloadKeysPathName()
	return path
}

func payloadKeysPathName() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve configuration directory: %w", err)
	}
	return filepath.Join(base, "babel", PayloadKeysName), nil
}

// LoadPayloadKeys reads the payload key document. A missing file is not an
// error: a local-mode deployment has no use for one, and a shared deployment
// that has not been given keys yet stages its output pending-sync and says so,
// which is a state SPEC.md §9 requires to be visible rather than fatal.
//
// Errors name the document and the offending field, never a value: every value
// in it is key material.
func LoadPayloadKeys() (PayloadKeys, bool, error) {
	path, err := payloadKeysPathName()
	if err != nil {
		return PayloadKeys{}, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return PayloadKeys{}, false, nil
	}
	if err != nil {
		return PayloadKeys{}, false, fmt.Errorf("open payload key document %s: %w", path, err)
	}
	defer f.Close()

	var keys PayloadKeys
	dec := json.NewDecoder(io.LimitReader(f, maxPayloadKeysFile))
	if err := dec.Decode(&keys); err != nil {
		return PayloadKeys{}, false, fmt.Errorf("decode payload key document %s: %w", path, redactKeyMaterial(err))
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PayloadKeys{}, false, fmt.Errorf("decode payload key document %s: more than one JSON value", path)
	}
	if keys.KeySchema > payloadKeySchema {
		return PayloadKeys{}, false, fmt.Errorf(
			"payload key document schema %d is newer than supported schema %d", keys.KeySchema, payloadKeySchema)
	}
	if err := ValidatePayloadKeys(keys); err != nil {
		return PayloadKeys{}, false, err
	}
	sort.Slice(keys.Keys, func(i, j int) bool { return keys.Keys[i].KeyID < keys.Keys[j].KeyID })
	return keys, true, nil
}

// redactKeyMaterial keeps a decoder's echo of a malformed value out of an
// error. encoding/json quotes the offending token in some syntax errors, and in
// this document every token is a key.
func redactKeyMaterial(err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Errorf("malformed JSON at byte offset %d", syntax.Offset)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Errorf("field %q is not a %s", typeErr.Field, typeErr.Type)
	}
	return errors.New("document is not a payload key document")
}

// ValidatePayloadKeys checks a document is usable before anything seals with
// it. It is exported so a writer validates before writing and a loader
// validates after reading, with one rule set.
func ValidatePayloadKeys(keys PayloadKeys) error {
	if len(keys.Keys) == 0 {
		return errors.New("payload key document carries no keys")
	}
	if len(keys.Keys) > maxPayloadKeys {
		return fmt.Errorf("payload key document carries %d keys, over the %d-key bound",
			len(keys.Keys), maxPayloadKeys)
	}
	if keys.ActiveKeyID == "" {
		return errors.New("payload key document active_key_id is required")
	}
	seen := make(map[string]bool, len(keys.Keys))
	active := false
	for _, k := range keys.Keys {
		if !validKeyID.MatchString(k.KeyID) {
			return fmt.Errorf("payload key document key_id %q is invalid: "+
				"key ids are 1-64 characters of [a-z0-9._-] starting alphanumeric", k.KeyID)
		}
		if seen[k.KeyID] {
			return fmt.Errorf("payload key document names key_id %q twice", k.KeyID)
		}
		seen[k.KeyID] = true
		material, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil {
			return fmt.Errorf("payload key %q is not standard base64", k.KeyID)
		}
		if len(material) != payloadKeyBytes {
			return fmt.Errorf("payload key %q decodes to %d bytes, and a key is %d",
				k.KeyID, len(material), payloadKeyBytes)
		}
		if k.KeyID == keys.ActiveKeyID {
			active = true
		}
	}
	if !active {
		return fmt.Errorf("payload key document active_key_id %q is not one of its keys", keys.ActiveKeyID)
	}
	return nil
}

// Material decodes every key in the document.
//
// It returns a map because that is what a ring is built from, and it decodes
// here rather than in the consumer so the base64 rule lives beside the document
// that carries it. The caller owns the returned bytes and may zero them once
// the ring holds them.
func (k PayloadKeys) Material() (active string, material map[string][]byte, err error) {
	if err := ValidatePayloadKeys(k); err != nil {
		return "", nil, err
	}
	material = make(map[string][]byte, len(k.Keys))
	for _, key := range k.Keys {
		bytes, err := base64.StdEncoding.DecodeString(key.Key)
		if err != nil {
			return "", nil, fmt.Errorf("payload key %q is not standard base64", key.KeyID)
		}
		material[key.KeyID] = bytes
	}
	return k.ActiveKeyID, material, nil
}

// SavePayloadKeys writes the document atomically at mode 0600, and refuses to
// replace one that exists.
//
// The refusal is the point. Every other configuration write in Babel replaces
// the previous document, because a locator or a credential is a current value;
// a key document is a history, and losing an entry from it makes objects
// unreadable that nothing will ever rewrite. Adding a key is a read, an append,
// and a write of the whole document through AddPayloadKey.
func SavePayloadKeys(keys PayloadKeys) error {
	if err := ValidatePayloadKeys(keys); err != nil {
		return err
	}
	path, err := payloadKeysPathName()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s: %w", path, ErrPayloadKeysExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check payload key document %s: %w", path, err)
	}
	return writePayloadKeys(path, keys)
}

// AddPayloadKey appends a key to the document and makes it active, creating the
// document if there is none.
//
// This is rotation: the previous active key stays in the ring, so every object
// sealed under it keeps opening, and only new envelopes use the new one. That is
// internal/envelope's Rotate, made durable.
func AddPayloadKey(key PayloadKey, active bool) error {
	path, err := payloadKeysPathName()
	if err != nil {
		return err
	}
	keys, found, err := LoadPayloadKeys()
	if err != nil {
		return err
	}
	if !found {
		keys = PayloadKeys{KeySchema: payloadKeySchema}
	}
	for _, existing := range keys.Keys {
		if existing.KeyID == key.KeyID {
			return fmt.Errorf("payload key document already carries key_id %q", key.KeyID)
		}
	}
	keys.Keys = append(keys.Keys, key)
	if active || keys.ActiveKeyID == "" {
		keys.ActiveKeyID = key.KeyID
	}
	if err := ValidatePayloadKeys(keys); err != nil {
		return err
	}
	return writePayloadKeys(path, keys)
}

// writePayloadKeys replaces the document atomically. The containing directory
// and file are private to the current user, on the same terms as storage.json:
// this is the one file in Babel that is nothing but key material.
func writePayloadKeys(path string, keys PayloadKeys) error {
	keys.KeySchema = payloadKeySchema
	sort.Slice(keys.Keys, func(i, j int) bool { return keys.Keys[i].KeyID < keys.Keys[j].KeyID })

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "."+PayloadKeysName+"-*")
	if err != nil {
		return fmt.Errorf("create temporary payload key document: %w", err)
	}
	temp := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary payload key document: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(keys); err != nil {
		_ = f.Close()
		return errors.New("encode payload key document")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync payload key document: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close payload key document: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace payload key document %s: %w", path, err)
	}
	keep = true
	return nil
}

// GeneratePayloadKey returns a fresh key entry with id, reading key material
// from the caller.
//
// The material is supplied rather than generated here because this package
// implements no cryptography: internal/envelope owns GenerateKey, and a second
// place that produces key bytes is a second place that can produce them badly.
func GeneratePayloadKey(id string, material []byte) (PayloadKey, error) {
	if !validKeyID.MatchString(id) {
		return PayloadKey{}, fmt.Errorf("payload key id %q is invalid: "+
			"key ids are 1-64 characters of [a-z0-9._-] starting alphanumeric", id)
	}
	if len(material) != payloadKeyBytes {
		return PayloadKey{}, fmt.Errorf("payload key material is %d bytes, and a key is %d",
			len(material), payloadKeyBytes)
	}
	return PayloadKey{KeyID: id, Key: base64.StdEncoding.EncodeToString(material)}, nil
}
