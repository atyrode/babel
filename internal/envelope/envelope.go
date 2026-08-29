// Package envelope seals Babel's sensitive Phase B payloads before they leave
// the process, so PostgreSQL only ever receives ciphertext. SPEC.md 9 keeps a
// minimal plaintext allowlist in the shared catalog - structured identifiers,
// entity kind/schema version, encrypted-object references, key ID, ciphertext
// size, commit/sync state, and relationship IDs, the same vocabulary
// internal/sharedcatalog's Phase A allowlist enforces - and requires every
// other payload (titles, claims, operator context, findings, proposals, review
// notes, receipts) to travel as a randomized versioned AEAD envelope carrying
// associated identity/schema data and a key ID. This package is that envelope,
// and nothing else: it holds no key management UX, no storage integration, and
// no password-based derivation.
//
// The construction is AES-256-GCM from the standard library with a fresh
// random 96-bit nonce per message. That choice is deliberate on two counts. It
// adds no dependency to a public repository whose crypto is worth auditing by
// reading, and a random nonce keeps no counter, so an envelope needs no
// persisted state and two instances sealing concurrently cannot collide by
// forgetting to coordinate.
//
// The price of random nonces is a birthday bound, recorded here rather than
// discovered later. With a uniformly random 96-bit nonce, the probability that
// any two of q messages under one key repeat a nonce is about q^2 / 2^97, so
// the collision probability stops being negligible as q approaches 2^32
// messages (see MessagesPerKeyCeiling); a repeated nonce under GCM is
// catastrophic, leaking the XOR of two plaintexts and the authentication
// subkey. The mitigation is key rotation: a Keyring seals under one active key
// and opens under every key it knows, so retiring a key that has approached
// the ceiling costs nothing and leaves historical rows readable.
//
// Errors from this package name the failure and, where an operator needs it,
// the key ID; they never contain key material, plaintext, ciphertext, or
// associated-data values. Key IDs and algorithm names are escaped with
// strconv.Quote before they reach a message, because they arrive from durable
// rows and must not be able to smuggle terminal control sequences into a
// diagnostic.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Version is the envelope schema version this package writes. Open refuses any
// other version outright instead of parsing it optimistically: a future
// version may change what the associated data binds, and a wrong guess about
// that would authenticate a payload against the wrong location.
const Version = 1

// Algorithm names the AEAD construction, stored in every envelope so a reader
// never has to infer it from field sizes.
const Algorithm = "AES-256-GCM"

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// MessagesPerKeyCeiling is the number of messages a single key may seal before
// random 96-bit nonces make a collision probability worth worrying about (see
// the package comment's birthday bound). It is recorded as a constant so an
// operational guard can reference the number rather than restate the analysis.
// Rotation is the mitigation.
const MessagesPerKeyCeiling = 1 << 32

// maxAADField bounds each associated-data field. The bound exists so encoding
// cannot silently overflow a 32-bit length prefix, and because these fields
// are identifiers and column names, never payloads.
const maxAADField = 4096

// aadDomain is the fixed prefix of every associated-data encoding. It separates
// this construction from any other use of the same keys, and being constant it
// cannot contribute to an encoding ambiguity.
const aadDomain = "babel/envelope/aad/v1"

var (
	// ErrUnknownKey reports an envelope naming a key the ring does not hold.
	// It is deliberately distinguishable from ErrAuthentication because the
	// operator responses differ: fetch or restore the missing key, versus
	// treat the row as tampered with.
	ErrUnknownKey = errors.New("envelope: key id not in keyring")

	// ErrAuthentication reports that AES-GCM rejected the envelope. The cause
	// is indistinguishable by design - modified ciphertext, modified nonce, a
	// different key, or associated data naming a different row all land here.
	ErrAuthentication = errors.New("envelope: authentication failed")

	// ErrUnsupportedVersion reports an envelope whose schema version this
	// build does not implement.
	ErrUnsupportedVersion = errors.New("envelope: unsupported envelope version")

	// ErrUnsupportedAlgorithm reports an envelope naming an AEAD this build
	// does not implement.
	ErrUnsupportedAlgorithm = errors.New("envelope: unsupported algorithm")

	// ErrMalformed reports an envelope whose shape is wrong before any
	// authentication can be attempted: a nonce or ciphertext of impossible
	// length. It is separate from ErrAuthentication because it indicates a
	// storage or encoding fault rather than a forgery attempt.
	ErrMalformed = errors.New("envelope: malformed envelope")

	// ErrKeySize reports a key that is not an AES-256 key.
	ErrKeySize = errors.New("envelope: key must be 32 bytes")

	// ErrKeyIDRequired reports an empty key ID, which could not be recorded in
	// the catalog's plaintext key_id column or used to find the key again.
	ErrKeyIDRequired = errors.New("envelope: key id is required")

	// ErrDuplicateKeyID reports adding a key ID the ring already holds, which
	// would silently replace key material and make previously sealed rows
	// unopenable.
	ErrDuplicateKeyID = errors.New("envelope: key id already in keyring")

	// ErrNoKey reports a Keyring with no active key, so it can neither seal
	// nor be trusted to open.
	ErrNoKey = errors.New("envelope: keyring has no active key")

	// ErrAADIncomplete reports associated data that does not identify a
	// location. Type and ID are mandatory: without them an envelope is bound
	// to nothing and could be moved between rows undetected, which is the
	// whole property AAD exists to provide.
	ErrAADIncomplete = errors.New("envelope: associated data requires type and id")

	// ErrAADTooLong reports an associated-data field longer than maxAADField.
	ErrAADTooLong = errors.New("envelope: associated data field is too long")
)

// Envelope is a sealed payload. Every field is non-secret: the ciphertext and
// nonce are safe to store, and V, KeyID, and Alg are exactly the plaintext
// metadata SPEC.md 9 allows beside it. The JSON names are short because these
// documents are stored per row.
type Envelope struct {
	V     int    `json:"v"`
	KeyID KeyID  `json:"kid"`
	Alg   string `json:"alg"`
	Nonce []byte `json:"n"`
	CT    []byte `json:"ct"`
}

// KeyID names a key. It is an opaque operator-assigned identifier stored in
// plaintext beside the ciphertext, so an authorized instance can tell which key
// a row needs without decrypting anything.
type KeyID string

// AAD binds a ciphertext to its logical location so an envelope cannot be moved
// between rows or record types undetected. Type is the entity kind, ID its
// globally unique identifier, and Field the column or payload name; Field may
// be empty when the envelope carries an entity's whole payload rather than one
// named field.
type AAD struct {
	Type  string
	ID    string
	Field string
}

// encode serializes the associated data injectively, together with the envelope
// version and key ID that must also be authenticated.
//
// The wire form is the constant domain string, then the version as four
// big-endian bytes, then the key ID and each AAD field as a four-byte
// big-endian length followed by its bytes:
//
//	"babel/envelope/aad/v1" || u32(V) || lp(KeyID) || lp(Type) || lp(ID) || lp(Field)
//
// This is injective because the prefix and the version are fixed width, the
// number of length-prefixed fields is fixed, and every field's length is read
// before its bytes. Concatenation alone would not be: {Type:"ab", ID:"c"} and
// {Type:"a", ID:"bc"} would produce identical data and let an envelope open
// against the wrong row.
//
// Version and KeyID are inside the authenticated data rather than merely
// alongside it, so an attacker cannot relabel a row to claim a different schema
// version or a different key and have it still authenticate.
func (a AAD) encode(version int, id KeyID) ([]byte, error) {
	if a.Type == "" || a.ID == "" {
		return nil, ErrAADIncomplete
	}
	if id == "" {
		return nil, ErrKeyIDRequired
	}
	if version < 0 {
		return nil, fmt.Errorf("envelope: version %d: %w", version, ErrUnsupportedVersion)
	}
	fields := [...]string{string(id), a.Type, a.ID, a.Field}
	size := len(aadDomain) + 4
	for _, f := range fields {
		if len(f) > maxAADField {
			return nil, fmt.Errorf("envelope: %d bytes exceeds %d: %w", len(f), maxAADField, ErrAADTooLong)
		}
		size += 4 + len(f)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, aadDomain...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(version))
	for _, f := range fields {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(f)))
		buf = append(buf, f...)
	}
	return buf, nil
}

// Keyring holds one active key for sealing and every known key for opening,
// which is what makes rotation a non-event: adding a key and promoting it
// changes what new rows use without touching what old rows need.
//
// Key material lives only inside the cipher.AEAD values of an unexported map,
// so no Keyring field can reach a log line, an error, or json.Marshal. The
// caller may zero its own copy of a key as soon as Add or Rotate returns.
//
// A Keyring is safe for concurrent use once built: Seal and Open only read it.
// Add and Rotate mutate it and must not race with them.
type Keyring struct {
	active KeyID
	keys   map[KeyID]cipher.AEAD
}

// NewKeyring returns a ring whose single key is active for sealing.
func NewKeyring(id KeyID, key []byte) (*Keyring, error) {
	k := &Keyring{keys: make(map[KeyID]cipher.AEAD, 1)}
	if err := k.Add(id, key); err != nil {
		return nil, err
	}
	k.active = id
	return k, nil
}

// Add registers a key the ring may open with but will not seal under. It is how
// a retired key stays usable for historical rows.
func (k *Keyring) Add(id KeyID, key []byte) error {
	if id == "" {
		return ErrKeyIDRequired
	}
	if len(key) != KeySize {
		return fmt.Errorf("envelope: key is %d bytes: %w", len(key), ErrKeySize)
	}
	if k.keys == nil {
		k.keys = make(map[KeyID]cipher.AEAD, 1)
	}
	if _, ok := k.keys[id]; ok {
		return fmt.Errorf("envelope: %s: %w", strconv.Quote(string(id)), ErrDuplicateKeyID)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("envelope: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("envelope: build gcm: %w", err)
	}
	k.keys[id] = aead
	return nil
}

// Rotate adds a key and makes it the one Seal uses. Envelopes sealed under the
// previous active key keep opening, because that key remains in the ring.
func (k *Keyring) Rotate(id KeyID, key []byte) error {
	if err := k.Add(id, key); err != nil {
		return err
	}
	k.active = id
	return nil
}

// ActiveKeyID reports which key Seal will use, for diagnostics and for
// recording the key ID a pending row will carry.
func (k *Keyring) ActiveKeyID() KeyID { return k.active }

// KeyIDs lists every key the ring can open with, sorted. It reports identifiers
// only; there is no accessor for key material.
func (k *Keyring) KeyIDs() []KeyID {
	ids := make([]KeyID, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// String describes the ring without revealing key material, so a Keyring caught
// by a %v or %+v in a log line cannot leak a key.
func (k *Keyring) String() string {
	if k == nil {
		return "envelope.Keyring(nil)"
	}
	return fmt.Sprintf("envelope.Keyring{active:%s, keys:%d}", strconv.Quote(string(k.active)), len(k.keys))
}

// Seal encrypts plaintext under the active key, bound to aad. Two calls with
// identical arguments produce different ciphertext: the nonce is fresh random
// each time, which is what keeps the shared catalog free of the deterministic
// ciphertext that would let an observer match equal payloads across rows.
func (k *Keyring) Seal(aad AAD, plaintext []byte) (Envelope, error) {
	if k == nil || k.active == "" {
		return Envelope{}, ErrNoKey
	}
	aead, ok := k.keys[k.active]
	if !ok {
		return Envelope{}, fmt.Errorf("envelope: %s: %w", strconv.Quote(string(k.active)), ErrUnknownKey)
	}
	ad, err := aad.encode(Version, k.active)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("envelope: read nonce: %w", err)
	}
	return Envelope{
		V:     Version,
		KeyID: k.active,
		Alg:   Algorithm,
		Nonce: nonce,
		CT:    aead.Seal(nil, nonce, plaintext, ad),
	}, nil
}

// Open decrypts e, which must have been sealed under the same aad by a key the
// ring holds. It checks the version and algorithm before touching key material,
// reports a missing key distinctly from a forgery, and otherwise returns
// ErrAuthentication with no further detail - AES-GCM's own failure carries none,
// and inventing any would only describe the attacker's input back to them.
func (k *Keyring) Open(aad AAD, e Envelope) ([]byte, error) {
	if k == nil || len(k.keys) == 0 {
		return nil, ErrNoKey
	}
	if e.V != Version {
		return nil, fmt.Errorf("envelope: version %d: %w", e.V, ErrUnsupportedVersion)
	}
	if e.Alg != Algorithm {
		return nil, fmt.Errorf("envelope: %s: %w", strconv.Quote(e.Alg), ErrUnsupportedAlgorithm)
	}
	aead, ok := k.keys[e.KeyID]
	if !ok {
		return nil, fmt.Errorf("envelope: %s: %w", strconv.Quote(string(e.KeyID)), ErrUnknownKey)
	}
	if len(e.Nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("envelope: nonce is %d bytes, want %d: %w", len(e.Nonce), aead.NonceSize(), ErrMalformed)
	}
	if len(e.CT) < aead.Overhead() {
		return nil, fmt.Errorf("envelope: ciphertext is %d bytes, want at least %d: %w", len(e.CT), aead.Overhead(), ErrMalformed)
	}
	ad, err := aad.encode(e.V, e.KeyID)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, e.Nonce, e.CT, ad)
	if err != nil {
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

// GenerateKey returns a fresh AES-256 key. Callers own its lifetime; this
// package never writes a key anywhere.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("envelope: generate key: %w", err)
	}
	return key, nil
}
