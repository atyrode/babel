package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
)

// The ceremony document is what one custody path hands to one machine, and this
// file is that document.
//
// storage.json carries a locator and credentials; payload-keys.json carries key
// material. A fleet member needs both, and until now only the first arrived by
// ceremony: a second host was given a configuration and no ring, read every
// plaintext catalog row, and could open no record's content (#112). The answer
// is not a second ceremony. Two custody paths are two things to remember, and
// the one that gets forgotten is the one holding the keys, so the document that
// already carries this deployment's credentials carries its ring too.
//
// The delivered ring is transport, never destination. `babel storage configure`
// installs it into payload-keys.json at mode 0600 and writes the configuration
// half to storage.json; no key material is ever written into storage.json. That
// is what keeps `config_schema` 2 frozen (SPEC.md §14) while the ceremony still
// grows a new capability, and it keeps the two lifecycles apart: a locator is a
// current value an operator edits, a ring is an append-only history where losing
// one entry makes objects unreadable that nothing will ever rewrite. `babel
// sync` reads the ring from exactly where it has always read it.

// ConfigureDocument is the whole input `babel storage configure --from-json`
// accepts: the storage configuration, and optionally this deployment's payload
// key ring.
//
// Config is embedded rather than nested so the document an operator or a
// provisioning script writes stays flat, which is the shape every existing
// document already has: adding the ring must not invalidate the documents in
// custody today.
type ConfigureDocument struct {
	Config

	// PayloadKeys is the deployment's whole append-only ring, never the newest
	// key alone (#112, SPEC.md §657): a delivery carrying only the active key
	// would install a host that seals correctly and cannot open a single
	// historical record.
	//
	// It is a pointer so that "this document carries no ring" and "this
	// document carries an empty ring" are different statements. The first is
	// the ordinary case for every document written before this field existed,
	// and it leaves the machine's keys exactly as they are; the second is a
	// malformed document and is refused.
	PayloadKeys *PayloadKeys `json:"payload_keys,omitempty"`
}

// Validate checks both halves of the document before either is installed.
//
// The order matters more than it looks: `storage configure` validates
// everything it was given, then installs, so a document that cannot work never
// displaces a configuration that does (SPEC.md §9). A ring refused here costs
// the machine nothing.
func (d ConfigureDocument) Validate() error {
	if err := Validate(d.Config); err != nil {
		return err
	}
	if d.PayloadKeys == nil {
		return nil
	}
	if err := checkPayloadKeySchema(d.PayloadKeys.KeySchema); err != nil {
		return err
	}
	return ValidatePayloadKeys(*d.PayloadKeys)
}

// ErrPayloadKeyConflict reports two different keys carrying one key id.
//
// It is refused rather than resolved, in either direction. A key id is stored
// in plaintext beside every ciphertext in the shared catalog and is what
// selects the key that opens it, so two hosts disagreeing about which bytes an
// id names is a fork of the deployment's key space: whichever side loses,
// records sealed on it stop opening. Nothing on this machine can tell which
// side is authoritative, so it stops and says so rather than picking one.
var ErrPayloadKeyConflict = errors.New("payload key material differs from the key already held under that id")

// PayloadKeyInstall reports what installing a delivered ring did to this
// machine. Every field is a key id or a path: no field can carry key material,
// which is what makes the whole value safe to render.
type PayloadKeyInstall struct {
	// Path is the document the ring lives in.
	Path string

	// Added names the keys the delivery brought that this host did not hold.
	Added []string

	// AbsentFromDocument names the keys this host holds that the delivery does
	// not carry. They are kept - dropping one orphans every object sealed under
	// it, forever, because Babel deletes no remote object - but as far as this
	// document knows they exist on this disk and nowhere else, which is a
	// custody gap only the operator can close.
	AbsentFromDocument []string

	// ActiveKeyID is the key new envelopes seal under after the install. The
	// delivery names it, which is what makes fleet-wide rotation a re-provision
	// rather than a per-host edit.
	ActiveKeyID string

	// Changed reports whether the document on disk was rewritten. A
	// re-provision that delivers a ring the host already holds must not churn
	// key material, and provisioning output has to distinguish "nothing to do"
	// from "this host just gained a key it could not open records without".
	Changed bool
}

// InstallPayloadKeys merges a delivered ring into this machine's payload key
// document, creating the document when there is none.
//
// The merge is a union, and that is the whole design. The ring is append-only
// (#110) because every object ever sealed under a retired key still needs that
// key, so a key this host holds is never dropped because a delivery omitted it,
// and a key the delivery brings is never skipped because this host is already
// configured. Rotation is therefore an ordinary re-provision: add the new key
// to the delivered ring, name it active there, and every host starts sealing
// under it while every host keeps opening everything it already had.
//
// Two refusals are absolute. A delivered id whose material differs from the
// material held under that id is ErrPayloadKeyConflict, and nothing is written.
// A union over the document's key bound is refused by ValidatePayloadKeys
// before any write, for the same reason: this function's contract is that it
// either installs a ring that is a superset of what was there or leaves the
// document byte-identical.
//
// No error and no returned value carries key material.
func InstallPayloadKeys(delivered PayloadKeys) (PayloadKeyInstall, error) {
	if err := checkPayloadKeySchema(delivered.KeySchema); err != nil {
		return PayloadKeyInstall{}, err
	}
	if err := ValidatePayloadKeys(delivered); err != nil {
		return PayloadKeyInstall{}, err
	}
	path, err := payloadKeysPathName()
	if err != nil {
		return PayloadKeyInstall{}, err
	}
	held, found, err := LoadPayloadKeys()
	if err != nil {
		return PayloadKeyInstall{}, err
	}

	res := PayloadKeyInstall{Path: path, ActiveKeyID: delivered.ActiveKeyID}
	merged := PayloadKeys{ActiveKeyID: delivered.ActiveKeyID, Keys: slices.Clone(held.Keys)}

	// Comparison is on decoded bytes rather than on the stored strings. Base64
	// of 32 bytes has four unused bits in its last character, so two documents
	// can spell the same key differently - one written by hand, one written
	// here - and a string comparison would call that a key-space fork and
	// refuse a re-provision that had nothing wrong with it.
	heldMaterial := make(map[string][]byte, len(held.Keys))
	for _, k := range held.Keys {
		material, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil {
			// LoadPayloadKeys validated the document it just read, so this is
			// unreachable through it; it is still checked, because the
			// alternative is comparing a key against nothing and calling it
			// equal.
			return PayloadKeyInstall{}, fmt.Errorf("payload key %q in %s is not standard base64", k.KeyID, path)
		}
		heldMaterial[k.KeyID] = material
	}

	for _, key := range delivered.Keys {
		material, err := base64.StdEncoding.DecodeString(key.Key)
		if err != nil {
			return PayloadKeyInstall{}, fmt.Errorf("payload key %q is not standard base64", key.KeyID)
		}
		existing, ok := heldMaterial[key.KeyID]
		if !ok {
			merged.Keys = append(merged.Keys, key)
			heldMaterial[key.KeyID] = material
			res.Added = append(res.Added, key.KeyID)
			continue
		}
		if !bytes.Equal(existing, material) {
			return PayloadKeyInstall{}, fmt.Errorf("%s: payload key %q: %w", path, key.KeyID, ErrPayloadKeyConflict)
		}
	}

	for _, k := range held.Keys {
		if !slices.ContainsFunc(delivered.Keys, func(d PayloadKey) bool { return d.KeyID == k.KeyID }) {
			res.AbsentFromDocument = append(res.AbsentFromDocument, k.KeyID)
		}
	}

	// An install that changes nothing writes nothing. Re-provisioning is
	// routine - `atyrode provision babel` runs it on every new machine and
	// again whenever an operator repairs one - and rewriting the one file in
	// Babel that is nothing but key material, on every run, for no change, is
	// gratuitous risk.
	res.Changed = !found || len(res.Added) > 0 || held.ActiveKeyID != merged.ActiveKeyID
	if !res.Changed {
		return res, nil
	}
	if err := ValidatePayloadKeys(merged); err != nil {
		return PayloadKeyInstall{}, err
	}
	if err := writePayloadKeys(path, merged); err != nil {
		return PayloadKeyInstall{}, err
	}
	return res, nil
}
