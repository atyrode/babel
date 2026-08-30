package preflight

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/event"
)

// TestRedactionIsIdempotentAndStable covers the two properties §3's step 4
// depends on. A hosted run may be prepared more than once — a retry, a
// refinement, a second recipe over the same preparation — and text that
// changed on the second pass would produce a different input for the same
// corpus, which makes a receipt's source digest meaningless.
func TestRedactionIsIdempotentAndStable(t *testing.T) {
	for _, text := range probeCorpus() {
		once := Redact(text)
		twice := Redact(once)
		if once != twice {
			t.Errorf("redaction is not idempotent:\n once: %q\ntwice: %q", once, twice)
		}
		if again := Redact(text); again != once {
			t.Errorf("redaction is not stable across calls:\n%q\n%q", once, again)
		}
		if strings.Contains(text, "an ordinary") {
			if once != text {
				t.Errorf("redacted a record with no credential in it: %q", once)
			}
			continue
		}
		if once == text {
			t.Errorf("nothing was redacted in %q", text)
		}
	}
}

// TestRedactionRemovesEverySecretValue is the guarantee itself: the text a
// hosted run may see contains no window of any planted credential. Eight-byte
// windows rather than whole values, because a partially redacted secret is
// still a disclosed one.
func TestRedactionRemovesEverySecretValue(t *testing.T) {
	joined := strings.Join(probeCorpus(), "\n")
	redacted := Redact(joined)
	for name, value := range probeSecrets {
		for _, window := range windows(value, 8) {
			if strings.Contains(redacted, window) {
				t.Errorf("redacted text still carries %q from the %s fixture", window, name)
				break
			}
		}
	}
	// Context survives: redaction that removed the field name and the host
	// would produce material not worth sending.
	for _, keep := range []string{"api_key", "Authorization: Bearer", "postgres://probe-user", "@catalog.invalid"} {
		if !strings.Contains(redacted, keep) {
			t.Errorf("redaction removed the context %q, not just the credential", keep)
		}
	}
}

// TestTheSameSecretGetsTheSamePlaceholder is what makes a redacted transcript
// reviewable: a reader must be able to see that one credential recurred, and
// that two others are different, without seeing any of them.
func TestTheSameSecretGetsTheSamePlaceholder(t *testing.T) {
	text := "first " + probeVendorToken + " then " + probeAWSKeyID + " then " + probeVendorToken + " again"
	redacted := Redact(text)

	placeholders := placeholderPattern.FindAllString(redacted, -1)
	if len(placeholders) != 3 {
		t.Fatalf("%d placeholders in %q, want 3", len(placeholders), redacted)
	}
	if placeholders[0] != placeholders[2] {
		t.Errorf("the same credential twice yielded %q and %q", placeholders[0], placeholders[2])
	}
	if placeholders[0] == placeholders[1] {
		t.Errorf("two different credentials collided on %q", placeholders[0])
	}
	if got := Placeholder(probeVendorToken); got != placeholders[0] {
		t.Errorf("Placeholder(%q) = %q, but redaction substituted %q", probeVendorToken, got, placeholders[0])
	}

	// A finding's placeholder is the same string, so a reviewer can join a
	// report to the redacted text they were given.
	in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", text)
	rep := mustCheck(t, localRequest(in))
	seen := make(map[string]bool)
	for _, f := range findingsByCategory(rep, CategorySecret) {
		seen[f.Placeholder] = true
	}
	if !seen[placeholders[0]] || !seen[placeholders[1]] {
		t.Errorf("findings carry %v, redacted text carries %v", seen, placeholders)
	}
}

// TestRedactionCoversASecretThatStraddlesARecordBoundary is the case the
// corpus forces and a naive rule fails. A session log is JSONL, captures are
// crash-consistent per file (§6.1), and a pasted key is larger than one
// record, so an armour block routinely opens in one record and closes in
// another. Each record is redacted alone, so each end of the block has to be
// recognizable by itself.
func TestRedactionCoversASecretThatStraddlesARecordBoundary(t *testing.T) {
	head := "the operator pasted a key:\n" + armourBeginLine + "\n" + probeKeyBodyOne
	tail := probeKeyBodyTwo + "\n" + armourEndLine + "\nand then asked about the deploy"

	for _, part := range []struct {
		name string
		text string
		body string
	}{
		{"opening record", head, probeKeyBodyOne},
		{"closing record", tail, probeKeyBodyTwo},
	} {
		t.Run(part.name, func(t *testing.T) {
			redacted := Redact(part.text)
			for _, window := range windows(part.body, 8) {
				if strings.Contains(redacted, window) {
					t.Fatalf("half of a straddling key survived redaction: %q in %q", window, redacted)
				}
			}
			if !placeholderPattern.MatchString(redacted) {
				t.Fatalf("no placeholder was substituted: %q", redacted)
			}
		})
	}

	// Both halves are reported, each with its own locator, so local evidence
	// can recover the whole key from the archive.
	in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", head, tail)
	rep := mustCheck(t, localRequest(in))
	var lines []int
	for _, f := range findingsByCategory(rep, CategorySecret) {
		if f.Detector == detectorPrivateKey {
			lines = append(lines, f.Evidence.Locator().Line)
		}
	}
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Errorf("private-key findings on lines %v, want one per record", lines)
	}
}

// TestRedactionPreservesLineStructure: a placeholder is not the length of the
// value it replaces, so byte offsets inside a record necessarily move. What
// must not move is the line structure, because a multi-line value replaced by
// a single-line placeholder would shift every line after it.
func TestRedactionPreservesLineStructure(t *testing.T) {
	text := "before the key\n" + probePrivateKey + "\nafter the key"
	redacted := Redact(text)
	if got, want := strings.Count(redacted, "\n"), strings.Count(text, "\n"); got != want {
		t.Errorf("redaction changed the line count from %d to %d", want, got)
	}
	if !strings.HasPrefix(redacted, "before the key\n") || !strings.HasSuffix(redacted, "\nafter the key") {
		t.Errorf("redaction moved surrounding content: %q", redacted)
	}
}

// TestRedactEventKeepsTheLocatorToTheOriginal is §3's split, checked against
// the archive rather than against itself: after redaction the event's locator
// must still recover the original record bytes, digest and all. An
// implementation that rewrote the locator to describe the redacted text would
// have destroyed the only path back to the evidence.
func TestRedactEventKeepsTheLocatorToTheOriginal(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe",
		"an ordinary operator message",
		"the config had api_key = \""+probeAssignedKey+"\" in it")

	file, err := os.Open(in.Stream.Path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()

	var redactedAny bool
	err = event.Scan(file, in.Stream, func(e event.Event) error {
		out := RedactEvent(e)
		if out.Locator != e.Locator {
			t.Errorf("RedactEvent changed the locator: %+v -> %+v", e.Locator, out.Locator)
		}
		if out.Index != e.Index || out.Kind != e.Kind || out.Role != e.Role {
			t.Errorf("RedactEvent changed the event beyond its text: %+v -> %+v", e, out)
		}
		if strings.Contains(out.Text, probeAssignedKey) {
			t.Errorf("RedactEvent left the credential in the text: %q", out.Text)
		}
		if out.Text != e.Text {
			redactedAny = true
		}
		// The claim under test: the locator the redacted event still carries
		// recovers the original record, byte for byte.
		if got := recordDigestAt(t, in.Stream.Path, out.Locator.ByteOffset); got != out.Locator.Digest {
			t.Errorf("locator digest %s does not match the record at offset %d (%s)",
				out.Locator.Digest, out.Locator.ByteOffset, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if !redactedAny {
		t.Error("no event was redacted, so the locator claim is vacuous")
	}
}

// recordDigestAt reads the record beginning at offset and returns its
// SHA-256 in the hex form internal/event's locators use.
func recordDigestAt(t *testing.T, path string, offset int64) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, 0); err != nil {
		t.Fatalf("seek fixture: %v", err)
	}
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && line == "" {
		t.Fatalf("read record: %v", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(line, "\n")))
	return hex.EncodeToString(sum[:])
}

// TestRedactionLeavesAlreadyRedactedTextAlone: idempotence is structural here
// — a placeholder is recognized and skipped — rather than a happy accident of
// no detector matching the placeholder's own shape. A later detector added
// without this property would silently start rewriting its own output.
func TestRedactionLeavesAlreadyRedactedTextAlone(t *testing.T) {
	already := "the config had api_key = \"" + Placeholder(probeAssignedKey) + "\" in it"
	if got := Redact(already); got != already {
		t.Errorf("redacted an already-redacted text:\n%q\n%q", already, got)
	}
	in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", already)
	rep := mustCheck(t, localRequest(in))
	if secrets := findingsByCategory(rep, CategorySecret); len(secrets) != 0 {
		t.Errorf("reported a placeholder as a secret: %+v", secrets)
	}
}

// TestRedactWithRefusesThresholdsItCannotApply: the text a hosted run sees and
// the report that accompanies it must be produced by the same rules, so
// thresholds that Check would refuse cannot be silently replaced here.
func TestRedactWithRefusesThresholdsItCannotApply(t *testing.T) {
	th := DefaultThresholds()
	th.EntropyMinBits = 9
	if _, err := RedactWith("some text", th); err == nil {
		t.Fatal("RedactWith accepted an impossible entropy floor")
	}

	// A tuned floor is honoured rather than ignored, and it binds only the
	// heuristic: no entropy setting can suppress a format match.
	th = DefaultThresholds()
	th.EntropyMinBits = 7.9
	text := "the deploy log printed " + probeEntropyToken + " and pushed with " + probeVendorToken
	out, err := RedactWith(text, th)
	if err != nil {
		t.Fatalf("RedactWith: %v", err)
	}
	if !strings.Contains(out, probeEntropyToken) {
		t.Error("a raised entropy floor was ignored")
	}
	if strings.Contains(out, probeVendorToken) {
		t.Error("an entropy floor suppressed a structural detector")
	}
}
