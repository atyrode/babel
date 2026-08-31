package config

import (
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

// deliveredRing builds a ring as a ceremony would deliver it: whole, with a
// named active key.
func deliveredRing(active string, keys ...PayloadKey) PayloadKeys {
	return PayloadKeys{ActiveKeyID: active, Keys: keys}
}

// rotatedKey is a second key whose material differs from sentinelKey's, so a
// test can tell a retained key from a delivered one and a merge from a
// replacement.
func rotatedKey(id string) PayloadKey {
	return PayloadKey{KeyID: id, Key: base64.StdEncoding.EncodeToString(rotatedMaterial)}
}

// TestConfigureDocumentCarriesTheRingAndToleratesUnknownNames covers the
// document's decode contract.
//
// The ring rides in the same flat document the ceremony already writes, so a
// document written by a compatible newer Babel must stay readable here - the
// same tolerance storage.json's loader has, for the same reason - and the
// configuration half must be unaffected by the new field's presence.
func TestConfigureDocumentCarriesTheRingAndToleratesUnknownNames(t *testing.T) {
	body := fmt.Sprintf(`{
	  "config_schema": 2,
	  "mode": "local",
	  "repository": "/srv/babel/repo",
	  "password_file": "/etc/babel/repository-password",
	  "host_id": "workstation",
	  "future_field": {"kept": false},
	  "payload_keys": {
	    "key_schema": 1,
	    "active_key_id": "phase-b-2",
	    "keys": [
	      {"key_id": "phase-b-1", "key": %q},
	      {"key_id": "phase-b-2", "key": %q}
	    ]
	  }
	}`, base64.StdEncoding.EncodeToString(sentinelMaterial), base64.StdEncoding.EncodeToString(rotatedMaterial))

	var doc ConfigureDocument
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decode ceremony document: %v", err)
	}
	if doc.Repository != "/srv/babel/repo" || doc.HostID != "workstation" || doc.ConfigSchema != 2 {
		t.Fatalf("configuration half decoded as %+v", doc.Config)
	}
	if doc.PayloadKeys == nil {
		t.Fatal("the document carries a ring and the decoder dropped it")
	}
	if doc.PayloadKeys.ActiveKeyID != "phase-b-2" || len(doc.PayloadKeys.Keys) != 2 {
		t.Fatalf("ring decoded as %+v", *doc.PayloadKeys)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("a valid ceremony document was refused: %v", err)
	}

	// A document with no ring is the shape every machine in custody today has.
	// It must validate and must be distinguishable from an empty ring, because
	// the first leaves a host's keys alone and the second is malformed.
	var noRing ConfigureDocument
	if err := json.Unmarshal([]byte(`{"repository":"/srv/babel/repo","password_file":"/etc/babel/pw"}`), &noRing); err != nil {
		t.Fatal(err)
	}
	if noRing.PayloadKeys != nil {
		t.Fatalf("a document without payload_keys decoded a ring: %+v", noRing.PayloadKeys)
	}
	if err := noRing.Validate(); err != nil {
		t.Fatalf("a ringless document was refused: %v", err)
	}
}

// TestConfigureDocumentValidationRefusals proves the document is refused as a
// whole: a ring that cannot work must not reach a machine just because the
// configuration beside it was fine, and the reverse.
func TestConfigureDocumentValidationRefusals(t *testing.T) {
	valid := Config{Repository: "/srv/babel/repo", PasswordFile: "/etc/babel/pw"}
	sentinel := sentinelKey("phase-b-1")

	cases := []struct {
		name string
		doc  ConfigureDocument
	}{
		{"relative password file, valid ring", ConfigureDocument{
			Config:      Config{Repository: "/srv/babel/repo", PasswordFile: "relative"},
			PayloadKeys: &PayloadKeys{ActiveKeyID: "phase-b-1", Keys: []PayloadKey{sentinel}},
		}},
		{"ring from a newer build", ConfigureDocument{
			Config:      valid,
			PayloadKeys: &PayloadKeys{KeySchema: payloadKeySchema + 1, ActiveKeyID: "phase-b-1", Keys: []PayloadKey{sentinel}},
		}},
		{"empty ring", ConfigureDocument{Config: valid, PayloadKeys: &PayloadKeys{}}},
		{"active key not in the ring", ConfigureDocument{
			Config:      valid,
			PayloadKeys: &PayloadKeys{ActiveKeyID: "phase-b-9", Keys: []PayloadKey{sentinel}},
		}},
		{"key material of the wrong length", ConfigureDocument{
			Config: valid,
			PayloadKeys: &PayloadKeys{ActiveKeyID: "phase-b-1", Keys: []PayloadKey{
				{KeyID: "phase-b-1", Key: base64.StdEncoding.EncodeToString(sentinelMaterial[:31])},
			}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.doc.Validate(); err == nil {
				t.Fatalf("ConfigureDocument.Validate accepted %s", tc.name)
			}
		})
	}
}

// TestInstallPayloadKeysCreatesTheRing is the new-machine case: a host that has
// never held a key is given the deployment's whole ring by one ceremony, at the
// permissions the document requires.
func TestInstallPayloadKeysCreatesTheRing(t *testing.T) {
	configHome(t)

	got, err := InstallPayloadKeys(deliveredRing("phase-b-2", sentinelKey("phase-b-1"), rotatedKey("phase-b-2")))
	if err != nil {
		t.Fatalf("InstallPayloadKeys: %v", err)
	}
	if !got.Changed {
		t.Error("installing a ring on a machine that held none reported no change")
	}
	if got.Path != PayloadKeysPath() {
		t.Errorf("Path = %q, want %q", got.Path, PayloadKeysPath())
	}
	slices.Sort(got.Added)
	if !slices.Equal(got.Added, []string{"phase-b-1", "phase-b-2"}) {
		t.Errorf("Added = %v, want both delivered keys", got.Added)
	}
	if len(got.AbsentFromDocument) != 0 {
		t.Errorf("AbsentFromDocument = %v on a machine that held nothing", got.AbsentFromDocument)
	}
	if got.ActiveKeyID != "phase-b-2" {
		t.Errorf("ActiveKeyID = %q, want the delivered active key", got.ActiveKeyID)
	}

	info, err := os.Stat(PayloadKeysPath())
	if err != nil {
		t.Fatalf("the ring was not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("installed ring mode = %04o, want 0600", mode)
	}
	if mode := mustStat(t, filepath.Dir(PayloadKeysPath())).Mode().Perm(); mode != 0o700 {
		t.Errorf("configuration directory mode = %04o, want 0700", mode)
	}

	keys, found, err := LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("the installed ring does not load back: found=%v err=%v", found, err)
	}
	if keys.ActiveKeyID != "phase-b-2" || len(keys.Keys) != 2 {
		t.Fatalf("installed ring = %+v", keys)
	}
	// The whole history, not the newest key alone (#112): a host given only
	// the active key seals correctly and opens no historical record.
	active, material, err := keys.Material()
	if err != nil {
		t.Fatal(err)
	}
	if active != "phase-b-2" || len(material) != 2 {
		t.Fatalf("Material = (%q, %d keys)", active, len(material))
	}
}

// TestInstallPayloadKeysIsIdempotent covers the case that runs most often:
// `atyrode provision babel` re-run on a machine that is already configured.
//
// The one file in Babel that is nothing but key material must not be rewritten
// for no change, and provisioning output has to be able to say "nothing to do".
func TestInstallPayloadKeysIsIdempotent(t *testing.T) {
	configHome(t)
	ring := deliveredRing("phase-b-1", sentinelKey("phase-b-1"))

	if _, err := InstallPayloadKeys(ring); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}

	got, err := InstallPayloadKeys(ring)
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if got.Changed {
		t.Error("re-delivering the ring a host already holds reported a change")
	}
	if len(got.Added) != 0 || len(got.AbsentFromDocument) != 0 {
		t.Errorf("re-provision reported Added=%v AbsentFromDocument=%v", got.Added, got.AbsentFromDocument)
	}
	after, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("an install that changed nothing rewrote the payload key document")
	}
}

// TestInstallPayloadKeysMergesAndNamesWhatCustodyLacks is the operator's
// backfill case, and the reason the merge is a union.
//
// The workstation generated a key before this ceremony existed, so the
// delivered ring does not carry it. Dropping it would orphan every object
// sealed under it - Babel deletes no remote object, so those objects would stay
// in Cellar forever and unreadable by anything - and silently keeping it would
// hide that the key exists on exactly one disk. So it is kept and named.
func TestInstallPayloadKeysMergesAndNamesWhatCustodyLacks(t *testing.T) {
	configHome(t)
	if err := AddPayloadKey(sentinelKey("workstation-1"), true); err != nil {
		t.Fatalf("seed the pre-ceremony key: %v", err)
	}

	got, err := InstallPayloadKeys(deliveredRing("vault-1", rotatedKey("vault-1")))
	if err != nil {
		t.Fatalf("InstallPayloadKeys: %v", err)
	}
	if !got.Changed {
		t.Error("a delivery carrying an unheld key reported no change")
	}
	if !slices.Equal(got.Added, []string{"vault-1"}) {
		t.Errorf("Added = %v, want the delivered key", got.Added)
	}
	if !slices.Equal(got.AbsentFromDocument, []string{"workstation-1"}) {
		t.Errorf("AbsentFromDocument = %v, want the pre-ceremony key", got.AbsentFromDocument)
	}

	keys, found, err := LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("LoadPayloadKeys = (found=%v, err=%v)", found, err)
	}
	if len(keys.Keys) != 2 {
		t.Fatalf("the merged ring holds %d keys, want both: %+v", len(keys.Keys), keys.Keys)
	}
	if !slices.ContainsFunc(keys.Keys, func(k PayloadKey) bool { return k.KeyID == "workstation-1" }) {
		t.Fatal("the install dropped a key the delivery omitted, orphaning everything sealed under it")
	}
	// Sealing follows custody: the delivered ring names the active key, which
	// is what makes fleet-wide rotation a re-provision rather than an edit on
	// every host. The retained key still opens what it sealed.
	if keys.ActiveKeyID != "vault-1" {
		t.Errorf("ActiveKeyID = %q, want the delivered active key", keys.ActiveKeyID)
	}
}

// TestInstallPayloadKeysRefusesConflictingMaterial pins the one refusal.
//
// Two different keys under one id is a fork of the deployment's key space: the
// id is what selects the opening key, so whichever side loses, records sealed
// on it stop opening. Nothing here can tell which side is authoritative, so it
// must refuse and leave the document byte-identical.
func TestInstallPayloadKeysRefusesConflictingMaterial(t *testing.T) {
	configHome(t)
	if err := AddPayloadKey(sentinelKey("phase-b-1"), true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}

	_, err = InstallPayloadKeys(deliveredRing("phase-b-1", rotatedKey("phase-b-1")))
	if !errors.Is(err, ErrPayloadKeyConflict) {
		t.Fatalf("conflicting material returned %v, want ErrPayloadKeyConflict", err)
	}
	if !strings.Contains(err.Error(), "phase-b-1") {
		t.Errorf("the refusal does not name the key id an operator has to reconcile: %q", err)
	}
	after, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a refused install rewrote the payload key document")
	}
}

// TestInstallPayloadKeysComparesMaterialNotSpelling defends a re-provision
// against a false conflict.
//
// Base64 of 32 bytes leaves four unused bits in its last character, so the same
// key can be spelled two ways - one written here, one written by hand or by
// another encoder - and a comparison on the stored string would call that a
// key-space fork and refuse a ceremony that had nothing wrong with it.
func TestInstallPayloadKeysComparesMaterialNotSpelling(t *testing.T) {
	configHome(t)
	canonical := sentinelKey("phase-b-1")
	if err := AddPayloadKey(canonical, true); err != nil {
		t.Fatal(err)
	}

	// The same 32 bytes, spelled with different unused bits in the final
	// character. Found rather than hard-coded so the fixture cannot drift from
	// what the encoder actually produces.
	alternate := respelledBase64(t, canonical.Key)
	got, err := InstallPayloadKeys(deliveredRing("phase-b-1", PayloadKey{KeyID: "phase-b-1", Key: alternate}))
	if err != nil {
		t.Fatalf("a differently spelled copy of the same key was refused: %v", err)
	}
	if got.Changed || len(got.Added) != 0 {
		t.Errorf("the same key spelled differently was treated as new: %+v", got)
	}
}

// respelledBase64 returns a different base64 spelling of the same bytes,
// failing the test if none exists rather than passing vacuously.
func respelledBase64(t *testing.T, encoded string) string {
	t.Helper()
	want, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := strings.IndexByte(encoded, '=')
	if last <= 0 {
		t.Fatalf("encoded key has no padding, so it has no spare bits to vary: %d characters", len(encoded))
	}
	for i := range alphabet {
		candidate := encoded[:last-1] + alphabet[i:i+1] + encoded[last:]
		if candidate == encoded {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(candidate)
		if err != nil || string(got) != string(want) {
			continue
		}
		return candidate
	}
	t.Fatal("no alternate base64 spelling of the same bytes exists, so this test proves nothing")
	return ""
}

// TestInstallPayloadKeysRefusesOverTheKeyBound proves the union is bounded
// before it is written: a delivery that would push the ring over the document's
// key bound is refused with the document intact, rather than writing a document
// its own loader would reject.
func TestInstallPayloadKeysRefusesOverTheKeyBound(t *testing.T) {
	configHome(t)
	held := make([]PayloadKey, maxPayloadKeys)
	for i := range held {
		held[i] = PayloadKey{KeyID: fmt.Sprintf("held-%03d", i), Key: sentinelKey("x").Key}
	}
	if err := writePayloadKeys(PayloadKeysPath(), PayloadKeys{ActiveKeyID: "held-000", Keys: held}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := InstallPayloadKeys(deliveredRing("vault-1", rotatedKey("vault-1"))); err == nil {
		t.Fatal("an install over the key bound succeeded, so the document's own loader would refuse it")
	}
	after, err := os.ReadFile(PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a refused install rewrote the payload key document")
	}
}
