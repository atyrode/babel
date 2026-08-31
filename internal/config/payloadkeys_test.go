package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sentinelMaterial is 32 bytes of recognizable key material. Tests search
// error strings for its raw bytes and its base64 encoding to confirm no
// refusal path echoes key material - the promise redactKeyMaterial exists to
// keep.
var sentinelMaterial = []byte("SENTINEL-KEY-MATERIAL-32-BYTES!!")

// rotatedMaterial is a second, distinct 32-byte key, used wherever a test
// needs two keys that must remain distinguishable (rotation, Material).
var rotatedMaterial = []byte("ROTATED-KEY-MATERIAL-32-BYTES!!!")

// sentinelKey returns a valid PayloadKey carrying sentinelMaterial under id.
func sentinelKey(id string) PayloadKey {
	return PayloadKey{KeyID: id, Key: base64.StdEncoding.EncodeToString(sentinelMaterial)}
}

func marshalDoc(t *testing.T, keys PayloadKeys) []byte {
	t.Helper()
	b, err := json.Marshal(keys)
	if err != nil {
		t.Fatalf("marshal payload key document: %v", err)
	}
	return b
}

func TestLoadMissingDocumentIsNotAnError(t *testing.T) {
	configHome(t)
	keys, found, err := LoadPayloadKeys()
	if err != nil {
		t.Fatalf("LoadPayloadKeys: %v", err)
	}
	if found {
		t.Fatal("LoadPayloadKeys reported found for an absent document")
	}
	if len(keys.Keys) != 0 || keys.ActiveKeyID != "" {
		t.Fatalf("LoadPayloadKeys returned %+v for an absent document, want zero value", keys)
	}
}

func TestSaveLoadRoundTripSortsKeys(t *testing.T) {
	home := configHome(t)
	want := PayloadKeys{
		ActiveKeyID: "mike",
		Keys: []PayloadKey{
			sentinelKey("zulu"),
			sentinelKey("alpha"),
			{KeyID: "mike", Key: base64.StdEncoding.EncodeToString(rotatedMaterial)},
		},
	}
	if err := SavePayloadKeys(want); err != nil {
		t.Fatalf("SavePayloadKeys: %v", err)
	}
	if got, wantPath := PayloadKeysPath(), filepath.Join(home, "babel", PayloadKeysName); got != wantPath {
		t.Fatalf("PayloadKeysPath = %q, want %q", got, wantPath)
	}

	got, found, err := LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("LoadPayloadKeys = (%+v, %v, %v)", got, found, err)
	}
	if got.ActiveKeyID != "mike" {
		t.Errorf("ActiveKeyID = %q, want %q", got.ActiveKeyID, "mike")
	}
	ids := make([]string, len(got.Keys))
	for i, k := range got.Keys {
		ids[i] = k.KeyID
	}
	// Keys is written in an arbitrary order (zulu, alpha, mike) but must come
	// back sorted by key id, since two machines given the same document must
	// read the same order.
	if want := []string{"alpha", "mike", "zulu"}; !slices.Equal(ids, want) {
		t.Fatalf("Keys ids = %v, want %v", ids, want)
	}
}

func TestSaveWritesPrivateModes(t *testing.T) {
	configHome(t)
	if err := SavePayloadKeys(PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{sentinelKey("k1")}}); err != nil {
		t.Fatalf("SavePayloadKeys: %v", err)
	}
	info, err := os.Stat(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("payload-keys.json mode = %04o, want 0600", got)
	}
	if got := mustStat(t, filepath.Dir(PayloadKeysPath())).Mode().Perm(); got != 0o700 {
		t.Errorf("configuration directory mode = %04o, want 0700", got)
	}
}

func TestSaveRefusesToReplaceExistingDocument(t *testing.T) {
	configHome(t)
	if err := SavePayloadKeys(PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{sentinelKey("k1")}}); err != nil {
		t.Fatalf("SavePayloadKeys: %v", err)
	}
	before, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}

	replacement := PayloadKeys{ActiveKeyID: "k2", Keys: []PayloadKey{{
		KeyID: "k2", Key: base64.StdEncoding.EncodeToString(rotatedMaterial),
	}}}
	if err := SavePayloadKeys(replacement); !errors.Is(err, ErrPayloadKeysExist) {
		t.Fatalf("SavePayloadKeys over existing document: %v, want ErrPayloadKeysExist", err)
	}
	after, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("SavePayloadKeys over an existing document altered its bytes")
	}
}

func TestAddPayloadKeyCreatesThenRotates(t *testing.T) {
	configHome(t)
	if err := AddPayloadKey(sentinelKey("k1"), true); err != nil {
		t.Fatalf("AddPayloadKey create: %v", err)
	}
	got, found, err := LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("LoadPayloadKeys after create = (%+v, %v, %v)", got, found, err)
	}
	if got.ActiveKeyID != "k1" || len(got.Keys) != 1 {
		t.Fatalf("after create: ActiveKeyID=%q Keys=%v, want k1 and one key", got.ActiveKeyID, got.Keys)
	}

	second := PayloadKey{KeyID: "k2", Key: base64.StdEncoding.EncodeToString(rotatedMaterial)}
	if err := AddPayloadKey(second, true); err != nil {
		t.Fatalf("AddPayloadKey rotate: %v", err)
	}
	got, found, err = LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("LoadPayloadKeys after rotate = (%+v, %v, %v)", got, found, err)
	}
	if got.ActiveKeyID != "k2" {
		t.Errorf("ActiveKeyID after rotate = %q, want %q", got.ActiveKeyID, "k2")
	}
	if len(got.Keys) != 2 {
		t.Fatalf("Keys after rotate = %v, want 2 entries", got.Keys)
	}
	// The rotation property: the predecessor must still be present, or every
	// object it ever sealed becomes permanently unreadable.
	if !slices.ContainsFunc(got.Keys, func(k PayloadKey) bool { return k.KeyID == "k1" }) {
		t.Fatal("AddPayloadKey rotation dropped the previous key")
	}
}

func TestAddPayloadKeyRefusesDuplicateID(t *testing.T) {
	configHome(t)
	if err := AddPayloadKey(sentinelKey("k1"), true); err != nil {
		t.Fatalf("AddPayloadKey: %v", err)
	}
	if err := AddPayloadKey(sentinelKey("k1"), false); err == nil {
		t.Fatal("AddPayloadKey accepted a duplicate key id")
	}
}

func TestValidatePayloadKeysRefusals(t *testing.T) {
	validB64 := base64.StdEncoding.EncodeToString(sentinelMaterial)
	tooMany := make([]PayloadKey, maxPayloadKeys+1)
	for i := range tooMany {
		tooMany[i] = PayloadKey{KeyID: fmt.Sprintf("k%03d", i), Key: validB64}
	}

	cases := []struct {
		name string
		keys PayloadKeys
	}{
		{"no keys", PayloadKeys{ActiveKeyID: "k1"}},
		{"over the key bound", PayloadKeys{ActiveKeyID: "k000", Keys: tooMany}},
		{"empty active_key_id", PayloadKeys{Keys: []PayloadKey{{KeyID: "k1", Key: validB64}}}},
		{"active_key_id not among keys", PayloadKeys{ActiveKeyID: "missing", Keys: []PayloadKey{{KeyID: "k1", Key: validB64}}}},
		{"uppercase key id", PayloadKeys{ActiveKeyID: "K1", Keys: []PayloadKey{{KeyID: "K1", Key: validB64}}}},
		{"leading punctuation key id", PayloadKeys{ActiveKeyID: "-k1", Keys: []PayloadKey{{KeyID: "-k1", Key: validB64}}}},
		{"over-length key id", PayloadKeys{ActiveKeyID: strings.Repeat("a", 65), Keys: []PayloadKey{{KeyID: strings.Repeat("a", 65), Key: validB64}}}},
		{"empty key id", PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "", Key: validB64}}}},
		{"duplicate key id", PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: validB64}, {KeyID: "k1", Key: validB64}}}},
		{"not standard base64", PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: "not-base64!"}}}},
		{"decodes to 31 bytes", PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: base64.StdEncoding.EncodeToString(sentinelMaterial[:31])}}}},
		{"decodes to 33 bytes", PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: base64.StdEncoding.EncodeToString(append(bytes.Clone(sentinelMaterial), 0))}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePayloadKeys(tc.keys); err == nil {
				t.Fatalf("ValidatePayloadKeys(%s) succeeded, want a refusal", tc.name)
			}
		})
	}
}

func TestLoadRefusesNewerSchema(t *testing.T) {
	configHome(t)
	if err := os.MkdirAll(filepath.Dir(PayloadKeysPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf(`{"key_schema":2,"active_key_id":"k1","keys":[{"key_id":"k1","key":%q}]}`,
		base64.StdEncoding.EncodeToString(sentinelMaterial))
	if err := os.WriteFile(PayloadKeysPath(), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadPayloadKeys()
	if err == nil {
		t.Fatal("LoadPayloadKeys accepted a document newer than the supported schema")
	}
	// The message must name both numbers, since that is the pair an operator
	// needs to decide whether to upgrade Babel or roll back the document.
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1") {
		t.Errorf("schema refusal %q does not name both schema numbers", err.Error())
	}
}

func TestLoadRefusesTrailingContent(t *testing.T) {
	configHome(t)
	if err := os.MkdirAll(filepath.Dir(PayloadKeysPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := marshalDoc(t, PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{sentinelKey("k1")}})
	both := append(append([]byte{}, doc...), doc...)
	if err := os.WriteFile(PayloadKeysPath(), both, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPayloadKeys(); err == nil {
		t.Fatal("LoadPayloadKeys accepted a document with two JSON values")
	}
}

func TestMaterialDecodesEveryKey(t *testing.T) {
	keys := PayloadKeys{
		ActiveKeyID: "k2",
		Keys: []PayloadKey{
			sentinelKey("k1"),
			{KeyID: "k2", Key: base64.StdEncoding.EncodeToString(rotatedMaterial)},
		},
	}
	active, material, err := keys.Material()
	if err != nil {
		t.Fatalf("Material: %v", err)
	}
	if active != "k2" {
		t.Errorf("active = %q, want %q", active, "k2")
	}
	if len(material) != 2 {
		t.Fatalf("material has %d entries, want 2", len(material))
	}
	for id, b := range material {
		if len(b) != payloadKeyBytes {
			t.Errorf("material[%q] is %d bytes, want %d", id, len(b), payloadKeyBytes)
		}
	}
	if !bytes.Equal(material["k1"], sentinelMaterial) {
		t.Error("material[k1] does not match the stored key")
	}

	if _, _, err := (PayloadKeys{}).Material(); err == nil {
		t.Fatal("Material on an invalid document succeeded")
	}
}

func TestGeneratePayloadKey(t *testing.T) {
	if _, err := GeneratePayloadKey("Bad Id", sentinelMaterial); err == nil {
		t.Fatal("GeneratePayloadKey accepted an invalid key id")
	}
	if _, err := GeneratePayloadKey("k1", sentinelMaterial[:31]); err == nil {
		t.Fatal("GeneratePayloadKey accepted 31 bytes of material")
	}
	key, err := GeneratePayloadKey("k1", sentinelMaterial)
	if err != nil {
		t.Fatalf("GeneratePayloadKey: %v", err)
	}
	if key.KeyID != "k1" {
		t.Errorf("KeyID = %q, want %q", key.KeyID, "k1")
	}
	decoded, err := base64.StdEncoding.DecodeString(key.Key)
	if err != nil {
		t.Fatalf("decode generated key: %v", err)
	}
	if !bytes.Equal(decoded, sentinelMaterial) {
		t.Error("GeneratePayloadKey's base64 does not decode back to the material handed in")
	}
}

// TestNoKeyMaterialEscapes is the audit-facing guarantee for this document:
// every refusal below involves real key bytes somewhere in its input, and
// none of them may echo those bytes - raw or base64-encoded - into an error.
// That is the whole reason redactKeyMaterial exists, and this is what pins it.
func TestNoKeyMaterialEscapes(t *testing.T) {
	configHome(t)
	sentinelRaw := string(sentinelMaterial)
	sentinelB64 := base64.StdEncoding.EncodeToString(sentinelMaterial)

	var subjects []string
	record := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected a refusal, got a nil error")
		}
		subjects = append(subjects, err.Error())
	}

	// ValidatePayloadKeys: invalid base64, wrong-length material.
	record(ValidatePayloadKeys(PayloadKeys{
		ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: sentinelRaw}}, // hyphens are not base64
	}))
	record(ValidatePayloadKeys(PayloadKeys{
		ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: base64.StdEncoding.EncodeToString(sentinelMaterial[:31])}},
	}))

	// Material: the same base64 failure surfaces here too.
	_, _, err := (PayloadKeys{ActiveKeyID: "k1", Keys: []PayloadKey{{KeyID: "k1", Key: sentinelRaw}}}).Material()
	record(err)

	// AddPayloadKey: a duplicate key id, with real material attached to both
	// the seed and the rejected entry.
	if err := AddPayloadKey(PayloadKey{KeyID: "k1", Key: sentinelB64}, true); err != nil {
		t.Fatalf("AddPayloadKey seed: %v", err)
	}
	record(AddPayloadKey(PayloadKey{KeyID: "k1", Key: sentinelB64}, false))

	// LoadPayloadKeys: a decode failure whose invalid token IS the sentinel,
	// and a type-mismatch failure carrying the sentinel as its bad value.
	// Both go through redactKeyMaterial, which must not echo the token.
	syntaxDoc := fmt.Sprintf(`{"key_schema":1,"active_key_id":"k1","keys":[{"key_id":"k1","key":%s}]}`, sentinelRaw)
	if err := os.WriteFile(PayloadKeysPath(), []byte(syntaxDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadPayloadKeys()
	record(err)

	typeDoc := fmt.Sprintf(`{"key_schema":%q,"active_key_id":"k1","keys":[{"key_id":"k1","key":%q}]}`, sentinelRaw, sentinelB64)
	if err := os.WriteFile(PayloadKeysPath(), []byte(typeDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadPayloadKeys()
	record(err)

	if len(subjects) != 6 {
		t.Fatalf("collected %d error strings, want 6", len(subjects))
	}

	encodings := map[string]string{"raw": sentinelRaw, "base64": sentinelB64}
	for _, s := range subjects {
		for name, enc := range encodings {
			if strings.Contains(s, enc) {
				t.Errorf("%s key material leaked into error: %q", name, s)
			}
		}
	}
}
