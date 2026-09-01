package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// These cases drive `babel tell` through the shipped command surface — Run, the
// real durable file, the real stores, the real retrieval index — rather than
// through internal/complaint underneath it. What they check is the wiring: a
// store that captures a complaint and a command that never reaches it, or that
// reaches it and shows the operator nothing, is the failure a package test
// cannot see.

// TestTellCapturesAComplaintAndShowsWhatIsAdjacent walks what the operator
// actually does: say something, and be shown what Babel already has touching it.
//
// The adjacency pass is the reason the verb exists rather than a convenience on
// top of it (#115). An operator who is told "your complaint is recorded" learns
// nothing; one who is told "here are the three things Babel already holds about
// this" learns immediately whether they are repeating themselves, and Babel
// spends nothing to say it.
func TestTellCapturesAComplaintAndShowsWhatIsAdjacent(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("tell", "--operator", "synthetic-operator", "--json",
		"the repository rules keep getting ignored by every agent")
	first := decodeJSON[tellResult](t, stdout)
	switch {
	case !strings.HasPrefix(first.ID, "cmp_"):
		t.Errorf("complaint id = %q, want a cmp_ identifier", first.ID)
	case first.RootID != first.ID || first.Sequence != 1 || first.Supersedes != "":
		t.Errorf("first telling = %+v, want a first wording that amends nothing", first)
	case first.By != "synthetic-operator":
		t.Errorf("attributed to %q", first.By)
	case first.Host == "":
		t.Error("the capture host is absent, so the fleet cannot tell where this was said")
	case len(first.Adjacent) != 0:
		t.Errorf("the first complaint on a machine has %+v adjacent", first.Adjacent)
	case first.AdjacencyNote != "":
		t.Errorf("adjacency reported a fault: %s", first.AdjacencyNote)
	}
	assertNoRawControls(t, "tell --json", stdout, stderr)

	// A second complaint over the same words finds the first, which is only
	// possible because capture indexed it.
	stdout, stderr = f.ok("tell", "--operator", "synthetic-operator", "--json",
		"my repository rules are ignored again by the agent")
	second := decodeJSON[tellResult](t, stdout)
	if len(second.Adjacent) == 0 {
		t.Fatal("the second complaint found nothing adjacent, so capture indexed nothing")
	}
	found := false
	for _, row := range second.Adjacent {
		if row.ID == first.ID {
			found = true
			if row.Kind != "complaint" {
				t.Errorf("adjacent kind = %q, want complaint", row.Kind)
			}
			if row.Summary == "" {
				t.Error("the adjacent row has no summary, so it names an id and nothing else")
			}
		}
	}
	if !found {
		t.Errorf("adjacency = %+v, want the earlier complaint in it", second.Adjacent)
	}
	// A complaint is never adjacent to itself: matching yourself is not prior
	// material.
	for _, row := range second.Adjacent {
		if row.ID == second.ID {
			t.Error("the complaint was listed as adjacent to itself")
		}
	}
	assertNoRawControls(t, "tell --json second", stdout, stderr)
}

// TestTellSurfacesBabelsOwnPriorOutput is the other half of adjacency, and the
// half that makes it steering rather than a diary.
//
// The complaint and the analysis live in different stores and are indexed
// through one surface, so a complaint about the deploy finds the candidate Babel
// already minted about the deploy. If these were two indexes, capture would show
// the operator only their own earlier complaints — which they already remember.
func TestTellSurfacesBabelsOwnPriorOutput(t *testing.T) {
	f := newFixture(t)
	f.seed()

	stdout, _ := f.ok("tell", "--operator", "synthetic-operator", "--json",
		"the deferred candidate keeps coming back and I do not want it")
	res := decodeJSON[tellResult](t, stdout)
	for _, row := range res.Adjacent {
		if row.Kind == "hypothesis" {
			return
		}
	}
	t.Errorf("adjacency = %+v (note %q), want a hypothesis from the frontier in it",
		res.Adjacent, res.AdjacencyNote)
}

// TestTellReadsTheBodyFromStdin covers the paragraph case.
//
// An argument is how somebody says one sentence at the moment it annoys them; a
// heredoc is how they say the three sentences it actually takes. Both are the
// operator writing, and the newlines they typed survive into the record while
// every other control character does not.
func TestTellReadsTheBodyFromStdin(t *testing.T) {
	f := newFixture(t)
	const body = "the handoff drops my constraints\nand then the agent asks me to repeat them\n"

	stdout, stderr, code := f.runStdin(body, "tell", "--operator", "synthetic-operator", "--json")
	if code != exitOK {
		t.Fatalf("tell from stdin exited %d: %s", code, stderr)
	}
	res := decodeJSON[tellResult](t, stdout)
	if !strings.Contains(res.Text, "\n") {
		t.Errorf("stored text = %q, want the operator's own line break kept", res.Text)
	}
	if !strings.Contains(res.Text, "repeat them") {
		t.Errorf("stored text = %q, want the whole body", res.Text)
	}
	assertNoRawControls(t, "tell from stdin", stdout, stderr)
}

// hostileComplaint is what an operator pastes when they are quoting the thing
// that annoyed them: an SGR sequence, an OSC introducer, a bidi override, and a
// newline that would otherwise let a value forge a second output line.
//
// It is phaseb_test.go's fixture minus the raw C1 byte, and the difference is
// the subject of TestTellRefusesBytesItCannotSealFaithfully below: that byte is
// not valid UTF-8, and a complaint is refused rather than mangled.
const hostileComplaint = "\x1b[31mred\x1b[0m \x1b]0;retitled\x07 \u009b2J \u202egnitirw-thgir\u202c\nforged: line"

// TestTellEscapesHostileTextWithoutLosingTheParagraph covers §8's terminal
// safety against the one value in this package that is deliberately multi-line.
//
// The operator's newline is layout they supplied on purpose and is kept; an ESC,
// a bidi override or a C1 control in the same sentence is not, and each is
// escaped exactly as it would be anywhere else. A renderer that kept both would
// let a pasted transcript retitle the terminal; one that kept neither would
// print a paragraph as a line of escapes.
func TestTellEscapesHostileTextWithoutLosingTheParagraph(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("tell", "--operator", "synthetic-operator", "--json", hostileComplaint)
	assertNoRawControlsInProse(t, "tell hostile --json", stdout, stderr)
	res := decodeJSON[tellResult](t, stdout)
	if strings.Contains(res.Text, "\x1b") || strings.Contains(res.Text, "\u202e") {
		t.Errorf("stored text carries a raw control: %q", res.Text)
	}
	if !strings.Contains(res.Text, "\n") {
		t.Errorf("stored text = %q, want the fixture's own newline kept as layout", res.Text)
	}

	// The terminal rendering carries no raw control either, and it prints the
	// body rather than an escape of the whole of it.
	stdout, stderr = f.ok("tell", "--operator", "synthetic-operator", hostileComplaint)
	assertNoRawControlsInProse(t, "tell hostile", stdout, stderr)
	if !strings.Contains(stdout, "forged: line") {
		t.Errorf("terminal output dropped the body: %q", stdout)
	}
}

// assertNoRawControlsInProse is assertNoRawControls without its forged-line
// clause, and the difference is the subject of the test above.
//
// The shared helper allows a newline between lines and forbids one inside a
// rendered value, which it checks by requiring the fixture's `forged: line` to
// arrive as an escaped `\u{A}`. That clause encodes "no value may contribute a
// newline" - true of every other command, and the exact property `babel tell`
// deliberately inverts: a complaint's paragraph breaks are layout an operator
// typed on purpose, kept by sanitizeProse and asserted as kept above. Reusing
// the helper here would assert that the feature does not work.
//
// Everything else it checks is the property that actually matters and is kept:
// the stream is valid UTF-8, and no escape-worthy rune reached the terminal
// raw.
func assertNoRawControlsInProse(t *testing.T, label, stdout, stderr string) {
	t.Helper()
	for name, stream := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if !utf8.ValidString(stream) {
			t.Errorf("%s: %s is not valid UTF-8", label, name)
		}
		for _, r := range stream {
			if r == '\n' {
				continue
			}
			if unsafeRune(r) {
				t.Fatalf("%s: %s carries raw U+%04X:\n%s", label, name, r, stream)
			}
		}
	}
}

// TestTellRefusesBytesItCannotSealFaithfully covers the one thing about a
// complaint's content that is checked.
//
// The text is JSON-encoded on its way into an immutable sealed object, and
// encoding/json replaces a byte that is not valid UTF-8 with U+FFFD. So
// accepting one would mean the object the fleet receives, and the row this
// machine keeps, hold something the operator did not write - silently, and
// unrecoverably, because analysis_records is insert-only. Refusing costs the
// operator one retry and keeps "this is what they said" true.
func TestTellRefusesBytesItCannotSealFaithfully(t *testing.T) {
	f := newFixture(t)

	_, stderr, code := f.run("tell", "--operator", "synthetic-operator", "a raw C1 byte: \x9b2J")
	if code == exitOK {
		t.Fatal("a complaint carrying invalid UTF-8 was captured, so the sealed object cannot be what was typed")
	}
	if !strings.Contains(stderr, "UTF-8") {
		t.Errorf("the refusal does not say what was wrong: %q", stderr)
	}
}

// TestTellAmendsWithoutClosingAnything covers the only mutation a complaint
// admits, and the absence that matters more.
//
// The operator may say it better later and the earlier wording stays readable.
// What no invocation can do is end a complaint: there is no --close, no
// --resolve and no --done, because #115's guard is that steering pressure has no
// lifecycle, and the moment it acquires one Babel is a ticket queue.
func TestTellAmendsWithoutClosingAnything(t *testing.T) {
	f := newFixture(t)

	stdout, _ := f.ok("tell", "--operator", "synthetic-operator", "--json", "the deploy is flaky")
	first := decodeJSON[tellResult](t, stdout)

	stdout, stderr := f.ok("tell", "--operator", "synthetic-operator", "--amend", first.ID, "--json",
		"the deploy is flaky only on the second attempt")
	amended := decodeJSON[tellResult](t, stdout)
	switch {
	case amended.Supersedes != first.ID:
		t.Errorf("amendment supersedes %q, want %q", amended.Supersedes, first.ID)
	case amended.RootID != first.RootID:
		t.Errorf("amendment root = %q, want the chain %q", amended.RootID, first.RootID)
	case amended.Sequence != 2:
		t.Errorf("amendment sequence = %d, want 2", amended.Sequence)
	}
	assertNoRawControls(t, "tell --amend", stdout, stderr)

	// Amending the superseded wording is refused rather than branched: two
	// current wordings would be two answers to what the operator says.
	if _, _, code := f.run("tell", "--operator", "synthetic-operator", "--amend", first.ID,
		"a competing second wording"); code == exitOK {
		t.Error("a superseded wording was amended a second time")
	}

	// No flag ends a complaint, on either the verb or the record it wrote.
	for _, flag := range []string{"--close", "--resolve", "--done", "--status"} {
		if _, _, code := f.run("tell", "--operator", "synthetic-operator", flag, first.ID,
			"closing this"); code == exitOK {
			t.Errorf("%s was accepted; a complaint has no lifecycle to end (#115)", flag)
		}
	}

	// And the machine-readable record carries no lifecycle field to set.
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("decode the result as an object: %v", err)
	}
	for _, banned := range []string{"status", "state", "resolved", "closed", "assignee", "priority"} {
		if _, present := raw[banned]; present {
			t.Errorf("the result carries %q; a complaint is steering pressure, not a work item", banned)
		}
	}
}

// TestTellNamesWhatItIsAboutOrRefusesTheIdentifier covers the courtesy edge and
// its one refusal.
//
// An operator who already knows which candidate annoyed them should not have to
// open its page to say so. The identifier's own prefix decides which record it
// names, on refFor's reasoning: the operator pastes an id a listing printed, and
// no mistyped kind can make the edge point at the wrong table.
func TestTellNamesWhatItIsAboutOrRefusesTheIdentifier(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.ok("tell", "--operator", "synthetic-operator",
		"--about", ids.hypothesis, "--json", "this candidate is not the problem I reported")
	res := decodeJSON[tellResult](t, stdout)
	if len(res.About) != 1 || res.About[0] != ids.hypothesis {
		t.Errorf("about = %v, want the candidate the operator named", res.About)
	}
	// The edge bound, which is what an empty stderr means here: an emission
	// that had been refused would have warned rather than failed, and a warning
	// nobody asserted is how the graph quietly stops being written.
	if stderr != "" {
		t.Errorf("naming a record that exists warned: %q", stderr)
	}
	// assertNoRawControls is deliberately not used on this output. The seeded
	// records carry phaseb_test.go's hostile fixture, and this response quotes
	// their retrieval SUMMARIES - which internal/frontier derives by collapsing
	// the fixture's newline to a space, on purpose. The value is safe; the
	// helper's forged-line heuristic looks for an escaped newline that the
	// summarizer legitimately removed upstream. The escaping itself is covered
	// against the raw wording by TestTellEscapesHostileTextWithoutLosingTheParagraph.

	if _, _, code := f.run("tell", "--operator", "synthetic-operator",
		"--about", "not-an-identifier", "about nothing"); code != exitUsage {
		t.Errorf("an --about that names no record exited %d, want %d", code, exitUsage)
	}
}

// TestTellRefusesWhatItCannotAttribute covers the refusals, and the case that
// must not be refused.
//
// A complaint that had to be well-formed to be heard would be a form, and #115
// is explicit that this is not one: nothing about the content is judged. What is
// refused is a complaint nobody wrote and one with no words, because neither is
// something Babel could act on or attribute.
func TestTellRefusesWhatItCannotAttribute(t *testing.T) {
	f := newFixture(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no operator", []string{"tell", "something is wrong"}},
		{"two arguments", []string{"tell", "--operator", "op", "one", "two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := f.mustExit(exitUsage, tc.args...)
			if stdout != "" {
				t.Errorf("a rejected invocation wrote to stdout: %q", stdout)
			}
		})
	}

	t.Run("no words", func(t *testing.T) {
		if _, _, code := f.run("tell", "--operator", "op", "   "); code == exitOK {
			t.Error("a complaint with no words in it was captured")
		}
	})

	// The case that must NOT be refused: a complaint naming nothing, about
	// nothing, on a machine that has analysed nothing.
	t.Run("free standing", func(t *testing.T) {
		if _, stderr := f.ok("tell", "--operator", "op", "this is all a bit much"); stderr != "" {
			t.Errorf("a free-standing complaint reported %q", stderr)
		}
	})
}
