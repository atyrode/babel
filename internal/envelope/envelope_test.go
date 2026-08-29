package envelope

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// markedKey is 32 bytes of recognizable material, so a test can search error
// strings and marshalled documents for any encoding of it.
var markedKey = []byte("SUPERSECRETKEYMATERIAL0123456789")

func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	k, err := NewKeyring("k1", markedKey)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return k
}

func testAAD() AAD {
	return AAD{Type: "hypothesis", ID: "01J0000000000000000000000A", Field: "title"}
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := testKeyring(t)
	cases := []struct {
		name      string
		aad       AAD
		plaintext []byte
	}{
		{"typical", testAAD(), []byte("a synthetic hypothesis title")},
		{"empty plaintext", testAAD(), []byte{}},
		{"nil plaintext", testAAD(), nil},
		{"no field", AAD{Type: "finding", ID: "f-1"}, []byte("whole payload")},
		{"binary plaintext", testAAD(), []byte{0x00, 0xff, 0x1b, 0x0a, 0x7f}},
		{"unicode identity", AAD{Type: "réalité", ID: "id\u00a01", Field: "note"}, []byte("payload")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := k.Seal(tc.aad, tc.plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if e.V != Version {
				t.Errorf("V = %d, want %d", e.V, Version)
			}
			if e.Alg != Algorithm {
				t.Errorf("Alg = %q, want %q", e.Alg, Algorithm)
			}
			if e.KeyID != "k1" {
				t.Errorf("KeyID = %q, want %q", e.KeyID, "k1")
			}
			if len(e.Nonce) != 12 {
				t.Errorf("nonce is %d bytes, want 12 (96-bit)", len(e.Nonce))
			}
			if len(e.CT) != len(tc.plaintext)+16 {
				t.Errorf("ciphertext is %d bytes, want plaintext+16 tag = %d", len(e.CT), len(tc.plaintext)+16)
			}
			if len(tc.plaintext) > 0 && bytes.Contains(e.CT, tc.plaintext) {
				t.Error("ciphertext contains the plaintext")
			}
			got, err := k.Open(tc.aad, e)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Errorf("plaintext = %q, want %q", got, tc.plaintext)
			}
		})
	}
}

// TestSealIsRandomized is the property SPEC.md 9 demands of the shared catalog:
// no deterministic ciphertext, so equal payloads cannot be matched across rows.
func TestSealIsRandomized(t *testing.T) {
	k := testKeyring(t)
	aad := testAAD()
	plaintext := []byte("identical payload")

	const runs = 32
	nonces := make(map[string]bool, runs)
	cts := make(map[string]bool, runs)
	for i := range runs {
		e, err := k.Seal(aad, plaintext)
		if err != nil {
			t.Fatalf("Seal %d: %v", i, err)
		}
		if nonces[string(e.Nonce)] {
			t.Fatalf("nonce repeated at seal %d", i)
		}
		if cts[string(e.CT)] {
			t.Fatalf("ciphertext repeated at seal %d", i)
		}
		nonces[string(e.Nonce)] = true
		cts[string(e.CT)] = true
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	k := testKeyring(t)
	// A second key in the ring lets the KeyID case reach authentication rather
	// than stopping at ErrUnknownKey; both outcomes are wrong for an attacker,
	// but only this one proves the key ID is authenticated.
	second, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := k.Add("k2", second); err != nil {
		t.Fatalf("Add: %v", err)
	}
	aad := testAAD()

	cases := []struct {
		name   string
		mutate func(*Envelope, *AAD)
	}{
		{"ciphertext body", func(e *Envelope, _ *AAD) { e.CT[0] ^= 0x01 }},
		{"ciphertext tag", func(e *Envelope, _ *AAD) { e.CT[len(e.CT)-1] ^= 0x01 }},
		{"ciphertext truncated", func(e *Envelope, _ *AAD) { e.CT = e.CT[:len(e.CT)-1] }},
		{"nonce", func(e *Envelope, _ *AAD) { e.Nonce[0] ^= 0x01 }},
		{"key id", func(e *Envelope, _ *AAD) { e.KeyID = "k2" }},
		{"aad type", func(_ *Envelope, a *AAD) { a.Type = "finding" }},
		{"aad id", func(_ *Envelope, a *AAD) { a.ID = "01J0000000000000000000000B" }},
		{"aad field", func(_ *Envelope, a *AAD) { a.Field = "body" }},
		{"aad field cleared", func(_ *Envelope, a *AAD) { a.Field = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := k.Seal(aad, []byte("payload"))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			e.Nonce = bytes.Clone(e.Nonce)
			e.CT = bytes.Clone(e.CT)
			got := aad
			tc.mutate(&e, &got)
			plaintext, err := k.Open(got, e)
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("Open error = %v, want ErrAuthentication", err)
			}
			if plaintext != nil {
				t.Errorf("Open returned %d plaintext bytes on failure", len(plaintext))
			}
		})
	}
}

// TestUnknownKeyIsDistinctFromTampering pins the distinction the operator acts
// on: fetch the missing key, versus treat the row as tampered with.
func TestUnknownKeyIsDistinctFromTampering(t *testing.T) {
	k := testKeyring(t)
	e, err := k.Seal(testAAD(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	e.KeyID = "k-retired"

	_, err = k.Open(testAAD(), e)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Open error = %v, want ErrUnknownKey", err)
	}
	if errors.Is(err, ErrAuthentication) {
		t.Error("unknown key also matches ErrAuthentication; the two must be distinguishable")
	}
	if !strings.Contains(err.Error(), "k-retired") {
		t.Errorf("error %q does not name the missing key id", err)
	}

	// And the converse: a genuine forgery must not look like a missing key.
	e2, err := k.Seal(testAAD(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	e2.CT[0] ^= 0x01
	_, err = k.Open(testAAD(), e2)
	if errors.Is(err, ErrUnknownKey) {
		t.Errorf("tampered envelope reported as ErrUnknownKey: %v", err)
	}
}

func TestOpenRejectsUnsupportedHeader(t *testing.T) {
	k := testKeyring(t)
	cases := []struct {
		name   string
		mutate func(*Envelope)
		want   error
	}{
		{"version zero", func(e *Envelope) { e.V = 0 }, ErrUnsupportedVersion},
		{"version future", func(e *Envelope) { e.V = Version + 1 }, ErrUnsupportedVersion},
		{"version negative", func(e *Envelope) { e.V = -1 }, ErrUnsupportedVersion},
		{"algorithm", func(e *Envelope) { e.Alg = "AES-128-GCM" }, ErrUnsupportedAlgorithm},
		{"algorithm empty", func(e *Envelope) { e.Alg = "" }, ErrUnsupportedAlgorithm},
		{"nonce short", func(e *Envelope) { e.Nonce = e.Nonce[:11] }, ErrMalformed},
		{"nonce long", func(e *Envelope) { e.Nonce = append(e.Nonce, 0) }, ErrMalformed},
		{"nonce absent", func(e *Envelope) { e.Nonce = nil }, ErrMalformed},
		{"ciphertext shorter than tag", func(e *Envelope) { e.CT = e.CT[:15] }, ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := k.Seal(testAAD(), []byte("payload"))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			tc.mutate(&e)
			if _, err := k.Open(testAAD(), e); !errors.Is(err, tc.want) {
				t.Fatalf("Open error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestRotation is the freeze item's rotation-compatibility requirement: new
// rows use the new key, old rows keep opening.
func TestRotation(t *testing.T) {
	k := testKeyring(t)
	aad := testAAD()
	old, err := k.Seal(aad, []byte("sealed before rotation"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	nextKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := k.Rotate("k2", nextKey); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got := k.ActiveKeyID(); got != "k2" {
		t.Errorf("ActiveKeyID = %q, want %q", got, "k2")
	}

	got, err := k.Open(aad, old)
	if err != nil {
		t.Fatalf("Open pre-rotation envelope: %v", err)
	}
	if string(got) != "sealed before rotation" {
		t.Errorf("plaintext = %q", got)
	}

	fresh, err := k.Seal(aad, []byte("sealed after rotation"))
	if err != nil {
		t.Fatalf("Seal after rotation: %v", err)
	}
	if fresh.KeyID != "k2" {
		t.Errorf("new envelope KeyID = %q, want %q", fresh.KeyID, "k2")
	}
	if got, err := k.Open(aad, fresh); err != nil || string(got) != "sealed after rotation" {
		t.Fatalf("Open post-rotation envelope: %q, %v", got, err)
	}

	// A ring holding only the new key cannot open the old row, and says so as a
	// missing key rather than as tampering.
	fresh2, err := NewKeyring("k2", nextKey)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if _, err := fresh2.Open(aad, old); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Open with rotated-away key: %v, want ErrUnknownKey", err)
	}

	if want := []KeyID{"k1", "k2"}; fmt.Sprint(k.KeyIDs()) != fmt.Sprint(want) {
		t.Errorf("KeyIDs = %v, want %v", k.KeyIDs(), want)
	}
}

// TestAADEncodingIsInjective walks adjacent splittings of the same concatenated
// characters. A naive concatenated encoding would collide on every pair here.
func TestAADEncodingIsInjective(t *testing.T) {
	variants := []AAD{
		{Type: "ab", ID: "cd", Field: "ef"},
		{Type: "a", ID: "bcd", Field: "ef"},
		{Type: "abc", ID: "d", Field: "ef"},
		{Type: "ab", ID: "c", Field: "def"},
		{Type: "ab", ID: "cde", Field: "f"},
		{Type: "abcd", ID: "e", Field: "f"},
		{Type: "a", ID: "b", Field: "cdef"},
		{Type: "a", ID: "bcdef", Field: ""},
		{Type: "abcde", ID: "f", Field: ""},
		{Type: "ab", ID: "cdef", Field: ""},
	}

	seen := make(map[string]AAD, len(variants))
	for _, a := range variants {
		encoded, err := a.encode(Version, "k1")
		if err != nil {
			t.Fatalf("encode %+v: %v", a, err)
		}
		if prev, ok := seen[string(encoded)]; ok {
			t.Fatalf("encoding collision: %+v and %+v", prev, a)
		}
		seen[string(encoded)] = a
	}

	// The key ID shares the same authenticated data, so moving the boundary
	// between it and the type must not collide with the baseline splitting
	// (key "k1", type "ab") that the table above already registered.
	for _, tc := range []struct{ id, typ string }{{"k", "1ab"}, {"k1a", "b"}} {
		encoded, err := AAD{Type: tc.typ, ID: "cd", Field: "ef"}.encode(Version, KeyID(tc.id))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if prev, ok := seen[string(encoded)]; ok {
			t.Fatalf("encoding collision across key id boundary: %+v and %v/%v", prev, tc.id, tc.typ)
		}
		seen[string(encoded)] = AAD{Type: tc.typ, ID: "cd", Field: "ef"}
	}

	// The consequence at the AEAD layer: every variant's envelope authenticates
	// under its own AAD and under no other.
	k := testKeyring(t)
	sealed := make([]Envelope, len(variants))
	for i, a := range variants {
		e, err := k.Seal(a, []byte("payload"))
		if err != nil {
			t.Fatalf("Seal %+v: %v", a, err)
		}
		sealed[i] = e
	}
	for i, e := range sealed {
		for j, a := range variants {
			_, err := k.Open(a, e)
			if i == j {
				if err != nil {
					t.Errorf("Open %+v under its own aad: %v", a, err)
				}
				continue
			}
			if !errors.Is(err, ErrAuthentication) {
				t.Errorf("envelope %+v opened under %+v: err = %v", variants[i], a, err)
			}
		}
	}
}

func TestAADValidation(t *testing.T) {
	k := testKeyring(t)
	cases := []struct {
		name string
		aad  AAD
		want error
	}{
		{"no type", AAD{ID: "id"}, ErrAADIncomplete},
		{"no id", AAD{Type: "hypothesis"}, ErrAADIncomplete},
		{"empty", AAD{}, ErrAADIncomplete},
		{"type too long", AAD{Type: strings.Repeat("t", maxAADField+1), ID: "id"}, ErrAADTooLong},
		{"id too long", AAD{Type: "t", ID: strings.Repeat("i", maxAADField+1)}, ErrAADTooLong},
		{"field too long", AAD{Type: "t", ID: "i", Field: strings.Repeat("f", maxAADField+1)}, ErrAADTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := k.Seal(tc.aad, []byte("payload")); !errors.Is(err, tc.want) {
				t.Fatalf("Seal error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestKeyringConstruction(t *testing.T) {
	if _, err := NewKeyring("", markedKey); !errors.Is(err, ErrKeyIDRequired) {
		t.Errorf("NewKeyring with empty id: %v, want ErrKeyIDRequired", err)
	}
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewKeyring("k1", make([]byte, n)); !errors.Is(err, ErrKeySize) {
			t.Errorf("NewKeyring with %d-byte key: %v, want ErrKeySize", n, err)
		}
	}
	k := testKeyring(t)
	if err := k.Add("k1", markedKey); !errors.Is(err, ErrDuplicateKeyID) {
		t.Errorf("Add duplicate id: %v, want ErrDuplicateKeyID", err)
	}
	if err := k.Rotate("k1", markedKey); !errors.Is(err, ErrDuplicateKeyID) {
		t.Errorf("Rotate to duplicate id: %v, want ErrDuplicateKeyID", err)
	}
	if got := k.ActiveKeyID(); got != "k1" {
		t.Errorf("ActiveKeyID after failed rotation = %q, want unchanged %q", got, "k1")
	}

	var empty Keyring
	if _, err := empty.Seal(testAAD(), []byte("payload")); !errors.Is(err, ErrNoKey) {
		t.Errorf("Seal on empty keyring: %v, want ErrNoKey", err)
	}
	if _, err := empty.Open(testAAD(), Envelope{V: Version, Alg: Algorithm}); !errors.Is(err, ErrNoKey) {
		t.Errorf("Open on empty keyring: %v, want ErrNoKey", err)
	}
	var nilRing *Keyring
	if _, err := nilRing.Seal(testAAD(), nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("Seal on nil keyring: %v, want ErrNoKey", err)
	}
	if _, err := nilRing.Open(testAAD(), Envelope{}); !errors.Is(err, ErrNoKey) {
		t.Errorf("Open on nil keyring: %v, want ErrNoKey", err)
	}
	if got := nilRing.String(); !strings.Contains(got, "nil") {
		t.Errorf("nil Keyring String = %q", got)
	}

	generated, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(generated) != KeySize {
		t.Errorf("GenerateKey returned %d bytes, want %d", len(generated), KeySize)
	}
	other, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if bytes.Equal(generated, other) {
		t.Error("GenerateKey returned the same key twice")
	}
}

// TestNoKeyMaterialEscapes is the audit-facing guarantee: key bytes must not
// reach an error, a formatted Keyring, or a marshalled envelope in any encoding.
func TestNoKeyMaterialEscapes(t *testing.T) {
	k := testKeyring(t)
	aad := testAAD()
	e, err := k.Seal(aad, []byte("a synthetic title"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tampered := e
	tampered.CT = bytes.Clone(e.CT)
	tampered.CT[0] ^= 0x01
	missing := e
	missing.KeyID = "k-absent"
	badVersion := e
	badVersion.V = 99
	badAlg := e
	badAlg.Alg = "rot13"
	badNonce := e
	badNonce.Nonce = e.Nonce[:4]

	var subjects []string
	record := func(err error) {
		if err != nil {
			subjects = append(subjects, err.Error())
		}
	}
	_, err = k.Open(aad, tampered)
	record(err)
	_, err = k.Open(aad, missing)
	record(err)
	_, err = k.Open(aad, badVersion)
	record(err)
	_, err = k.Open(aad, badAlg)
	record(err)
	_, err = k.Open(aad, badNonce)
	record(err)
	_, err = k.Open(AAD{}, e)
	record(err)
	record(k.Add("k1", markedKey))
	_, err = NewKeyring("k1", make([]byte, 3))
	record(err)
	if len(subjects) != 8 {
		t.Fatalf("collected %d error strings, want 8", len(subjects))
	}

	subjects = append(subjects,
		k.String(),
		fmt.Sprintf("%v", k),
		fmt.Sprintf("%+v", k),
		fmt.Sprintf("%v %+v", *k, k.keys),
	)
	envelopeJSON, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	keyringJSON, err := json.Marshal(k)
	if err != nil {
		t.Fatalf("marshal keyring: %v", err)
	}
	subjects = append(subjects, string(envelopeJSON), string(keyringJSON))
	if string(keyringJSON) != "{}" {
		t.Errorf("marshalled Keyring = %s, want {}", keyringJSON)
	}

	encodings := map[string]string{
		"raw":       string(markedKey),
		"hex":       hex.EncodeToString(markedKey),
		"hex upper": strings.ToUpper(hex.EncodeToString(markedKey)),
		"base64":    base64.StdEncoding.EncodeToString(markedKey),
		"base64url": base64.URLEncoding.EncodeToString(markedKey),
		"go bytes":  fmt.Sprintf("%v", markedKey),
		"prefix":    string(markedKey[:8]),
	}
	for _, s := range subjects {
		for name, enc := range encodings {
			if strings.Contains(s, enc) {
				t.Errorf("%s key material appears in %q", name, s)
			}
		}
	}

	// Nor may the plaintext appear in the stored document.
	if strings.Contains(string(envelopeJSON), "a synthetic title") {
		t.Errorf("plaintext appears in marshalled envelope: %s", envelopeJSON)
	}

	// A round trip through JSON must still open, since that is how a row is
	// stored and read back.
	var decoded Envelope
	if err := json.Unmarshal(envelopeJSON, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	got, err := k.Open(aad, decoded)
	if err != nil {
		t.Fatalf("Open after JSON round trip: %v", err)
	}
	if string(got) != "a synthetic title" {
		t.Errorf("plaintext = %q", got)
	}
}

// TestErrorsAreTerminalSafe covers SPEC.md 9's rule that no terminal-facing
// value may carry raw control sequences: key IDs and algorithm names arrive
// from durable rows, so they are escaped before reaching a message.
func TestErrorsAreTerminalSafe(t *testing.T) {
	k := testKeyring(t)
	e, err := k.Seal(testAAD(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	hostile := "\x1b[31mred\x07\u202ereversed"

	e.KeyID = KeyID(hostile)
	_, keyErr := k.Open(testAAD(), e)
	if !errors.Is(keyErr, ErrUnknownKey) {
		t.Fatalf("Open: %v, want ErrUnknownKey", keyErr)
	}

	e.KeyID = "k1"
	e.Alg = hostile
	_, algErr := k.Open(testAAD(), e)
	if !errors.Is(algErr, ErrUnsupportedAlgorithm) {
		t.Fatalf("Open: %v, want ErrUnsupportedAlgorithm", algErr)
	}

	for _, msg := range []string{keyErr.Error(), algErr.Error()} {
		for _, bad := range []string{"\x1b", "\x07", "\u202e"} {
			if strings.Contains(msg, bad) {
				t.Errorf("error %q contains raw control %q", msg, bad)
			}
		}
		if !strings.Contains(msg, `\x1b`) {
			t.Errorf("error %q does not show the escaped sequence", msg)
		}
	}
}

// TestLargePayload confirms sealing is linear rather than accidentally
// quadratic, and records the measured duration so a regression is visible in
// test output rather than only as a timeout.
func TestLargePayload(t *testing.T) {
	k := testKeyring(t)
	aad := testAAD()

	const size = 10 << 20
	plaintext := make([]byte, size)
	for i := range plaintext {
		plaintext[i] = byte(i * 7)
	}

	start := time.Now()
	e, err := k.Seal(aad, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed := time.Since(start)

	start = time.Now()
	got, err := k.Open(aad, e)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	opened := time.Since(start)

	if !bytes.Equal(got, plaintext) {
		t.Fatal("round trip changed the payload")
	}
	if len(e.CT) != size+16 {
		t.Errorf("ciphertext is %d bytes, want %d", len(e.CT), size+16)
	}
	t.Logf("10 MiB: seal %s, open %s", sealed.Round(time.Microsecond), opened.Round(time.Microsecond))
}

// TestMessagesPerKeyCeiling pins the documented birthday bound so the recorded
// operational constraint cannot drift silently.
func TestMessagesPerKeyCeiling(t *testing.T) {
	if MessagesPerKeyCeiling != 1<<32 {
		t.Errorf("MessagesPerKeyCeiling = %d, want 2^32", MessagesPerKeyCeiling)
	}
}
