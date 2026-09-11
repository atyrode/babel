// Package harness is the single declaration of the harness set Babel reads
// (SPEC.md §4.11, §6.8).
//
// A harness is a name and the record language its primary log is written
// in. Everything that must know which harnesses exist resolves it here:
// the scanner (internal/event), the transcript view
// (internal/transcript), the preparation validator (internal/run), and the
// source-adapter list (internal/cli). None of them carries its own list of
// names.
//
// That is not tidiness. Registering Babel's own analysis sessions as a
// harness previously took five unrelated edits, and a missed one failed at
// run time rather than at build time: `unknown harness "babel"` reached a
// running conductor cycle and ended it. With one declaration, adding a
// harness is one registration, and every consumer either resolves it here
// or fails to compile.
//
// The set is open by construction (SPEC.md §6.8). OMP, Codex and Claude
// Code are the first implementations, not the boundary. A harness whose
// log is written in a record language Babel already reads joins by
// registering its name and that format, with no other edit. A genuinely
// new record language additionally needs a reader in internal/event and
// internal/transcript, and the tests that walk this set are what name the
// missing reader.
package harness

import (
	"fmt"
	"sync"
)

// Format names the record language a harness writes its primary log in.
// Several harnesses can share one, so a format is not a harness: it is the
// grammar a reader is written against.
type Format string

// The declared record languages. A new value here is a new parser in
// internal/event and internal/transcript, which is why formats are
// enumerated while harnesses are registered.
const (
	FormatOMP    Format = "omp"
	FormatCodex  Format = "codex"
	FormatClaude Format = "claude"
)

// Valid reports whether f is a declared record language.
func (f Format) Valid() bool {
	switch f {
	case FormatOMP, FormatCodex, FormatClaude:
		return true
	default:
		return false
	}
}

// The stable lowercase harness names, as adapter.SourceSession.Harness and
// every catalog row carry them.
const (
	OMP    = "omp"
	Codex  = "codex"
	Claude = "claude"
	Babel  = "babel"
)

// Harness is one member of the set: the name every record of it is
// attributed to, and the record language its log speaks.
type Harness struct {
	Name   string
	Format Format
}

// declared is the harness set Babel ships with, in the order every surface
// lists it: the three harnesses Babel was built to read, then Babel's own.
//
// Babel's own analysis log is FormatOMP because its records are OMP's own
// message objects — an envelope of Babel's design would assert a schema
// over content Babel does not own — and it is a harness of its own rather
// than folded into OMP because provenance decides whether a session may be
// analysed at all, and a reader that could not tell Babel's reasoning from
// the corpus it reasoned over could not enforce that.
var declared = []Harness{
	{Name: OMP, Format: FormatOMP},
	{Name: Codex, Format: FormatCodex},
	{Name: Claude, Format: FormatClaude},
	{Name: Babel, Format: FormatOMP},
}

var (
	mu    sync.RWMutex
	set   = index(declared)
	order = names(declared)
)

func index(hs []Harness) map[string]Harness {
	out := make(map[string]Harness, len(hs))
	for _, h := range hs {
		out[h.Name] = h
	}
	return out
}

func names(hs []Harness) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

// Register adds a harness to the set. It is how a harness beyond the
// shipped four joins, and it is the only edit such a harness needs on the
// reading side when its records are in a language Babel already reads.
//
// Registration belongs in package initialization, before the first read: a
// harness appearing mid-scan would make a log's classification depend on
// when it was read rather than on what it is. A duplicate name is refused
// rather than overwritten, because two declarations of one name would make
// the record language a harness speaks ambiguous.
func Register(h Harness) error {
	if h.Name == "" {
		return fmt.Errorf("harness: name is required")
	}
	if !h.Format.Valid() {
		return fmt.Errorf("harness %s: undeclared record format %q", h.Name, h.Format)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := set[h.Name]; exists {
		return fmt.Errorf("harness %s: already registered", h.Name)
	}
	set[h.Name] = h
	order = append(order, h.Name)
	return nil
}

// Lookup returns the registered harness of that name. A miss is the one
// answer every consumer needs: an unrecognized harness name is not a
// degraded record, it is a caller naming something Babel does not read.
func Lookup(name string) (Harness, bool) {
	mu.RLock()
	defer mu.RUnlock()
	h, ok := set[name]
	return h, ok
}

// All returns the registered harnesses in registration order.
func All() []Harness {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Harness, 0, len(order))
	for _, name := range order {
		out = append(out, set[name])
	}
	return out
}
