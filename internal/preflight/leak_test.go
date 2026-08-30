package preflight_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/preflight"
)

// The whole point of a preflight report is that it can be read, stored, and
// shipped to a reviewer. A report that copied secrets out of the corpus would
// have moved the exposure rather than reported it, so this scans the serialized
// report for every sentinel independently of how the detectors are structured.
func TestReportNeverCarriesTheSecret(t *testing.T) {
	// Assembled rather than written whole: see the note in secret_test.go.
	// A format fixture cannot be a literal in a repository whose push
	// protection matches that format.
	sentinels := []string{
		"AKIA" + "IOSFODNN7SYNTH01",
		"ghp_" + "synthetic000000000000000000000000",
		"synthetic-password-value-not-real",
		"AIza" + "SyDsynthetic00000000000000000000000",
	}
	text := strings.Join([]string{
		"aws key " + sentinels[0],
		"token=" + sentinels[1],
		"postgres://user:" + sentinels[2] + "@host:5432/db",
		"google " + sentinels[3],
	}, "\n")

	redacted := preflight.Redact(text)
	for _, s := range sentinels {
		if strings.Contains(redacted, s) {
			t.Errorf("redacted text still contains %q", s)
		}
	}

	// The same secret twice must yield the same placeholder, so a reader can
	// see recurrence without seeing the value; two different secrets must not
	// collide, or recurrence would be fiction.
	twice := preflight.Redact(sentinels[0] + " and again " + sentinels[0])
	first := preflight.Placeholder(sentinels[0])
	if strings.Count(twice, first) != 2 {
		t.Errorf("the same secret did not yield the same placeholder twice: %q", twice)
	}
	if preflight.Placeholder(sentinels[0]) == preflight.Placeholder(sentinels[1]) {
		t.Error("two different secrets share a placeholder; recurrence would be fiction")
	}

	// Redaction must be idempotent: a redacted transcript passed through again
	// is unchanged, or a second pass would corrupt placeholders.
	if again := preflight.Redact(redacted); again != redacted {
		t.Errorf("redaction is not idempotent:\n first: %q\nsecond: %q", redacted, again)
	}

	// And the serialized form of a placeholder map must not smuggle values.
	blob, err := json.Marshal(map[string]string{"redacted": redacted})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, s := range sentinels {
		if strings.Contains(string(blob), s) {
			t.Errorf("serialized output contains %q", s)
		}
	}
}

// TestRedactionIsIdempotentForEveryDetector states idempotence as a property
// rather than a case. The bug this guards against was one detector matching
// inside its own placeholder — a credential-shaped field name followed by a
// placeholder reads as an assignment whose value is a long literal — so the
// class matters more than the instance, and a new detector must not be able to
// reintroduce it.
func TestRedactionIsIdempotentForEveryDetector(t *testing.T) {
	inputs := map[string]string{
		"aws access key":        "aws key " + "AKIA" + "IOSFODNN7SYNTH01" + " in text",
		"assignment":            "token=" + "ghp_" + "synthetic000000000000000000000000",
		"assignment quoted":     `api_key: "synthetic-secret-value-0001"`,
		"aws secret assignment": "aws_secret_access_key=synthetic0000000000000000000000000000000",
		"connection string":     "postgres://user:synthetic-password-0001@host:5432/db",
		"bearer":                "Authorization: Bearer synthetic-bearer-token-000001",
		"jwt":                   "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljc2ln",
		"google key":            "key " + "AIza" + "SyDsynthetic00000000000000000000000",
		"private key":           "-----BEGIN PRIVATE KEY-----\nc3ludGhldGljAAAA\n-----END PRIVATE KEY-----",
		"high entropy":          "value Zk3-Pq7_Lm2xVb9Nc4Rt6Yh8Wj1Ds5Gf0",
		"two of the same":       "token=" + "ghp_" + "synthetic000000000000000000000000 and " + "ghp_" + "synthetic000000000000000000000000",
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			once := preflight.Redact(in)
			twice := preflight.Redact(once)
			if once != twice {
				t.Errorf("not idempotent:\n once: %q\ntwice: %q", once, twice)
			}
			// A third pass catches a fix that merely delays the problem.
			if thrice := preflight.Redact(twice); thrice != once {
				t.Errorf("third pass differs:\n once: %q\nthrice: %q", once, thrice)
			}
			// No stray bracket may survive: a partial match inside a
			// placeholder is what left "]]" behind.
			if strings.Contains(twice, "]]]") {
				t.Errorf("placeholder corrupted by a second pass: %q", twice)
			}
		})
	}
}

// TestRecurrenceSurvivesASecondPass is the consequence that made the
// idempotence bug matter rather than merely being untidy: shared placeholders
// exist so a reviewer can see that one secret appeared twice without seeing it,
// and a second pass that re-digests one occurrence destroys exactly that.
func TestRecurrenceSurvivesASecondPass(t *testing.T) {
	const secret = "ghp_" + "synthetic000000000000000000000000"
	text := "token=" + secret + "\nheader: " + secret
	once := preflight.Redact(text)
	twice := preflight.Redact(once)
	want := preflight.Placeholder(secret)
	if got := strings.Count(twice, want); got != 2 {
		t.Errorf("after two passes the shared placeholder appears %d times, want 2: %q", got, twice)
	}
}
