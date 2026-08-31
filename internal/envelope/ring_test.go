package envelope

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRingFromSingleKey(t *testing.T) {
	ring, err := RingFrom("k1", map[KeyID][]byte{"k1": markedKey})
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}
	if got := ring.ActiveKeyID(); got != "k1" {
		t.Errorf("ActiveKeyID = %q, want %q", got, "k1")
	}
	if got := ring.KeyIDs(); !slices.Equal(got, []KeyID{"k1"}) {
		t.Errorf("KeyIDs = %v, want [k1]", got)
	}
	e, err := ring.Seal(testAAD(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := ring.Open(testAAD(), e)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, []byte("payload")) {
		t.Errorf("plaintext = %q, want %q", got, "payload")
	}
}

func TestRingFromManyKeys(t *testing.T) {
	other1 := bytes.Repeat([]byte{0x11}, KeySize)
	other2 := bytes.Repeat([]byte{0x22}, KeySize)
	set := map[KeyID][]byte{
		"zulu":  markedKey,
		"alpha": other1,
		"mike":  other2,
	}
	ring, err := RingFrom("mike", set)
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}
	if got := ring.ActiveKeyID(); got != "mike" {
		t.Errorf("ActiveKeyID = %q, want %q", got, "mike")
	}
	if got, want := ring.KeyIDs(), []KeyID{"alpha", "mike", "zulu"}; !slices.Equal(got, want) {
		t.Errorf("KeyIDs = %v, want %v", got, want)
	}

	aad := testAAD()
	e, err := ring.Seal(aad, []byte("sealed under mike"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := ring.Open(aad, e); err != nil {
		t.Fatalf("Open under active key: %v", err)
	}

	// The rotation guarantee: a second ring built from the same key set, but
	// with a different member active, must still open what the first ring
	// sealed - retiring an active key must never cost a historical row.
	other, err := RingFrom("alpha", set)
	if err != nil {
		t.Fatalf("RingFrom with different active key: %v", err)
	}
	if _, err := other.Open(aad, e); err != nil {
		t.Fatalf("Open by a ring with a different active key: %v", err)
	}
}

func TestRingFromRefusals(t *testing.T) {
	if _, err := RingFrom("", map[KeyID][]byte{"k1": markedKey}); !errors.Is(err, ErrKeyIDRequired) {
		t.Errorf("RingFrom with empty active id: %v, want ErrKeyIDRequired", err)
	}
	if _, err := RingFrom("k1", map[KeyID][]byte{}); err == nil {
		t.Error("RingFrom with an empty key map succeeded")
	}
	if _, err := RingFrom("missing", map[KeyID][]byte{"k1": markedKey}); !errors.Is(err, ErrActiveKeyMissing) {
		t.Errorf("RingFrom with an absent active id: %v, want ErrActiveKeyMissing", err)
	} else if !strings.Contains(err.Error(), "missing") {
		t.Errorf("ErrActiveKeyMissing %q does not name the missing id", err.Error())
	}
	if _, err := RingFrom("k1", map[KeyID][]byte{"k1": make([]byte, 31)}); !errors.Is(err, ErrKeySize) {
		t.Errorf("RingFrom with a 31-byte key: %v, want ErrKeySize", err)
	}
	if _, err := RingFrom("k1", map[KeyID][]byte{"k1": make([]byte, 33)}); !errors.Is(err, ErrKeySize) {
		t.Errorf("RingFrom with a 33-byte key: %v, want ErrKeySize", err)
	}
}

// TestRingFromIsDeterministic pins the doc comment's promise: two instances
// given the same key set build a ring whose KeyIDs are directly comparable,
// which is what lets a diagnostic that lists them agree across machines.
func TestRingFromIsDeterministic(t *testing.T) {
	set := map[KeyID][]byte{
		"zulu":  markedKey,
		"alpha": bytes.Repeat([]byte{0x33}, KeySize),
		"mike":  bytes.Repeat([]byte{0x44}, KeySize),
	}
	first, err := RingFrom("mike", set)
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}
	second, err := RingFrom("mike", set)
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}
	if !slices.Equal(first.KeyIDs(), second.KeyIDs()) {
		t.Errorf("KeyIDs differ across two rings from the same key set: %v vs %v", first.KeyIDs(), second.KeyIDs())
	}
}

// TestRingFromKeepsMaterialOutOfDiagnostics is RingFrom's share of the
// audit-facing guarantee envelope_test.go pins for the rest of the package:
// neither a construction error nor String() may reveal key bytes.
func TestRingFromKeepsMaterialOutOfDiagnostics(t *testing.T) {
	_, missingErr := RingFrom("missing", map[KeyID][]byte{"k1": markedKey})
	if missingErr == nil {
		t.Fatal("RingFrom with an absent active id succeeded")
	}
	ring, err := RingFrom("k1", map[KeyID][]byte{"k1": markedKey})
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}

	subjects := []string{missingErr.Error(), ring.String()}
	encodings := map[string]string{
		"raw":    string(markedKey),
		"hex":    hex.EncodeToString(markedKey),
		"base64": base64.StdEncoding.EncodeToString(markedKey),
	}
	for _, s := range subjects {
		for name, enc := range encodings {
			if strings.Contains(s, enc) {
				t.Errorf("%s key material appears in %s", name, strconv.Quote(s))
			}
		}
	}
}

// TestRingFromCopiesKeyMaterial pins the doc comment's promise that the
// caller may zero its own copy of every key as soon as RingFrom returns: the
// ring must hold its own cipher state, not a reference to the caller's slice.
func TestRingFromCopiesKeyMaterial(t *testing.T) {
	key := bytes.Clone(markedKey)
	ring, err := RingFrom("k1", map[KeyID][]byte{"k1": key})
	if err != nil {
		t.Fatalf("RingFrom: %v", err)
	}
	for i := range key {
		key[i] = 0
	}

	aad := testAAD()
	e, err := ring.Seal(aad, []byte("still sealable"))
	if err != nil {
		t.Fatalf("Seal after zeroing the caller's key: %v", err)
	}
	got, err := ring.Open(aad, e)
	if err != nil {
		t.Fatalf("Open after zeroing the caller's key: %v", err)
	}
	if !bytes.Equal(got, []byte("still sealable")) {
		t.Errorf("plaintext = %q, want %q", got, "still sealable")
	}
}
