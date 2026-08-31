package sharedcatalog

// This file exports the two identity predicates the Phase B commit protocol
// already applies, so a writer staging a record locally can refuse what
// SyncRun would refuse remotely.
//
// The reason they are exported at all is the failure they prevent. A record
// staged with an id or a kind PostgreSQL will not accept is a local
// pending-sync row that can never publish: every retry re-derives the same
// rejection, the operator sees a permanent backlog of one, and the remedy is
// not a retry but an edit to the writer. Refusing it at stage time turns that
// into a caller bug the writer's own test catches.
//
// They are thin wrappers over the unexported implementations rather than a
// second copy of the rules. A predicate restated in a second package is a
// predicate that eventually disagrees with itself, and disagreement here would
// mean a local journal accepting exactly what the remote protocol rejects.

// ValidEntityID reports whether s is a well-formed Phase B identifier: the same
// bound and character set validRecordID applies to every record and run id this
// package writes.
//
// The shape is stricter than PostgreSQL needs because these ids are also
// spliced into object-store keys, where a path separator or a traversal segment
// could name an object outside the namespace Babel manages.
func ValidEntityID(s string) bool { return validRecordID.MatchString(s) }

// Valid reports whether k is a record kind the shared catalog carries.
//
// The vocabulary is closed and the database CHECK in migrations/0003 holds
// exactly these values, so a new Phase B record type reaching PostgreSQL costs
// a migration and a review rather than arriving as an unnoticed string.
func (k RecordKind) Valid() bool { return k.valid() }
