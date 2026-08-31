package envelope

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// ErrActiveKeyMissing reports an active key id that is not among the keys
// offered. A ring that named a key it does not hold would seal nothing, and the
// failure would surface at the first publication rather than at construction.
var ErrActiveKeyMissing = errors.New("envelope: active key is not among the keys offered")

// RingFrom builds a Keyring from a deployment's whole key set in one call.
//
// It exists because every consumer of a stored key set was otherwise going to
// write the same loop - construct with the active key, add the rest, in an order
// that does not matter but has to be chosen - and two copies of that loop are
// two chances to add the keys and forget which one seals. The publisher and the
// fleet reader both need it, and neither owns the other.
//
// It reads key material and nothing else: no path, no file, no document. That
// keeps this package's promise that it holds no storage integration intact,
// while putting the one assembly step that both callers need in the package
// that owns what is being assembled. The caller may zero its own copy of every
// key as soon as this returns.
//
// A missing active key, a duplicate id, or a wrong key length is refused here.
// The ring is built in sorted id order so that two instances given the same key
// set construct the same ring, which makes a diagnostic that lists KeyIDs
// comparable between machines.
func RingFrom(active KeyID, keys map[KeyID][]byte) (*Keyring, error) {
	if active == "" {
		return nil, ErrKeyIDRequired
	}
	if len(keys) == 0 {
		return nil, errors.New("envelope: no keys offered")
	}
	if _, ok := keys[active]; !ok {
		return nil, fmt.Errorf("envelope: %s: %w", strconv.Quote(string(active)), ErrActiveKeyMissing)
	}

	ids := make([]KeyID, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	ring := &Keyring{}
	for _, id := range ids {
		if err := ring.Add(id, keys[id]); err != nil {
			return nil, err
		}
	}
	ring.active = active
	return ring, nil
}
