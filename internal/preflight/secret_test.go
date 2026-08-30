package preflight

import (
	"strings"
	"testing"
)

// Every credential below is synthetic and says so in its own bytes: each
// carries PROBEONLYNOTREAL or an obviously invented base64 body, so nothing
// here resembles a key that could exist. They are also the sentinels the
// leakage tests search for, which is why they are long and non-dictionary: a
// substring search must not match anything a summary, a path, or a digest
// emits by coincidence.
// A probe for a credential *format* can only be tested against a string in
// that format, so writing one as a whole literal makes the repository's own
// push protection reject every push that carries this file. The values below
// are assembled from parts for exactly that reason: the source never contains
// the matching byte sequence, while the assembled constant is byte-identical
// for the detectors under test. This is not obfuscation of a real credential —
// each one says PROBEONLYNOTREAL — it is the only way to hold a format fixture
// in a scanned repository.
const (
	probeAWSKeyID     = "AKIA" + "PROBEONLYNOTREAL"
	probeGoogleKey    = "AIza" + "PROBEONLYNOTREALGOOGLEKEY0123456789"
	probeVendorToken  = "ghp_" + "PROBEONLYNOTREALGITHUB01"
	probeJWT          = "eyJQUk9CRU9OTFk.eyJub3RSZWFsIjp0cnVlfQ.PROBEONLYNOTREALSIGNATURE"
	probeBearerToken  = "PROBEONLYNOTREALBEARER00"
	probeAssignedKey  = "PROBEONLYNOTREALAPIKEY01"
	probeURLPassword  = "PROBEONLYNOTREALPASSWORD1"
	probeEntropyToken = "7hK2mQ9vLxT4bN8wRzP3sYcE6dJf"

	// The armour body is split in two so one test can paste a whole key and
	// another can straddle a record boundary in the middle of it.
	probeKeyBodyOne = "UHJvYmVPbmx5Tm90UmVhbEtleU1hdGVyaWFsMDAwMQ"
	probeKeyBodyTwo = "VGFpbFByb2JlT25sZU5vdFJlYWxLZXlNYXRlcmlhbDAwMDI"

	armourBeginLine = "-----BEGIN OPENSSH PRIVATE KEY-----"
	armourEndLine   = "-----END OPENSSH PRIVATE KEY-----"
)

// probePrivateKey is a whole armour block in one record.
var probePrivateKey = armourBeginLine + "\n" + probeKeyBodyOne + "\n" + probeKeyBodyTwo + "\n" + armourEndLine

// probeSecrets is every value that must never appear in a report, keyed by the
// fixture that planted it.
var probeSecrets = map[string]string{
	"aws access key id":     probeAWSKeyID,
	"google api key":        probeGoogleKey,
	"vendor token":          probeVendorToken,
	"jwt":                   probeJWT,
	"bearer token":          probeBearerToken,
	"assigned api key":      probeAssignedKey,
	"connection password":   probeURLPassword,
	"entropy token":         probeEntropyToken,
	"private key body head": probeKeyBodyOne,
	"private key body tail": probeKeyBodyTwo,
}

// probeCorpus is one record's text per planted credential, plus an ordinary
// message so the corpus is not made entirely of secrets.
func probeCorpus() []string {
	return []string{
		"an ordinary operator message about a build",
		"the access key " + probeAWSKeyID + " was left in the log",
		"the google credentials line held " + probeGoogleKey + " once",
		"pushed with " + probeVendorToken + " from the workstation",
		"the request carried Authorization: Bearer " + probeBearerToken,
		"the session cookie decoded to " + probeJWT,
		`the config had api_key = "` + probeAssignedKey + `" in it`,
		"connected with postgres://probe-user:" + probeURLPassword + "@catalog.invalid:5432/babel",
		"the deploy log printed " + probeEntropyToken + " and exited",
		"the operator pasted a key:\n" + probePrivateKey,
	}
}

// TestEachDetectorFiresOnItsShape is the detector table, exercised through
// Check so that what is asserted is the finding an operator would see —
// detector, confidence, placeholder, and recoverable evidence — rather than an
// internal match.
//
// Each case asserts exactly one secret finding. A detector that also fired
// through a second rule would be a report that double-counts one credential,
// which is a defect and not a detail.
func TestEachDetectorFiresOnItsShape(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		detector   string
		confidence Confidence
	}{
		{
			name:       "aws access key id",
			text:       "the access key " + probeAWSKeyID + " was left in the log",
			detector:   "aws-access-key-id",
			confidence: ConfidenceStructural,
		},
		{
			name:       "google api key",
			text:       "the google credentials line held " + probeGoogleKey + " once",
			detector:   "google-api-key",
			confidence: ConfidenceStructural,
		},
		{
			name:       "vendor token",
			text:       "pushed with " + probeVendorToken + " from the workstation",
			detector:   "vendor-token",
			confidence: ConfidenceStructural,
		},
		{
			name:       "bearer credential",
			text:       "the request carried Authorization: Bearer " + probeBearerToken,
			detector:   "bearer-token",
			confidence: ConfidenceStructural,
		},
		{
			name:       "json web token",
			text:       "the session cookie decoded to " + probeJWT,
			detector:   "jwt",
			confidence: ConfidenceStructural,
		},
		{
			name:       "credential assignment",
			text:       `the config had api_key = "` + probeAssignedKey + `" in it`,
			detector:   "credential-assignment",
			confidence: ConfidenceStructural,
		},
		{
			name:       "connection string with an embedded password",
			text:       "connected with postgres://probe-user:" + probeURLPassword + "@catalog.invalid:5432/babel",
			detector:   "connection-string",
			confidence: ConfidenceStructural,
		},
		{
			name:       "private key armour block",
			text:       "the operator pasted a key:\n" + probePrivateKey,
			detector:   detectorPrivateKey,
			confidence: ConfidenceStructural,
		},
		{
			name:       "unstructured high-entropy token",
			text:       "the deploy log printed " + probeEntropyToken + " and exited",
			detector:   detectorEntropy,
			confidence: ConfidenceHeuristic,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", test.text)
			rep := mustCheck(t, localRequest(in))
			secrets := findingsByCategory(rep, CategorySecret)
			if len(secrets) != 1 {
				t.Fatalf("%d secret findings, want exactly 1: %+v", len(secrets), secrets)
			}
			got := secrets[0]
			if got.Detector != test.detector {
				t.Errorf("detector = %q, want %q", got.Detector, test.detector)
			}
			if got.Confidence != test.confidence {
				t.Errorf("confidence = %q, want %q", got.Confidence, test.confidence)
			}
			if got.Placeholder == "" || got.ValueBytes == 0 {
				t.Errorf("finding carries no placeholder or length: %+v", got)
			}
			loc := got.Evidence.Locator()
			if loc.Line != 1 || loc.Digest == "" || loc.Path != in.Stream.Path {
				t.Errorf("evidence does not locate the record: %+v", loc)
			}
			if got.Evidence.EventIndex() < 0 {
				t.Errorf("record evidence reported as whole-input evidence: %+v", got.Evidence)
			}
		})
	}
}

// probePublicKeyBody, probeInlineImage, and probeEmbeddedPayload are the dense
// base64 the near-miss cases are built from. They are generated rather than
// literal so the same bytes can be asserted both ways: quiet in context, and
// reported when that context is removed.
var (
	probePublicKeyBody   = strings.Repeat("QUJDZGVmR2hpSmts", 6)
	probeInlineImage     = strings.Repeat("QUJDZGVmR2hpSmts", 12)
	probeEmbeddedPayload = strings.Repeat("QUJDZGVmR2hpSmts", 80)
)

// TestNoDetectorFiresOnADocumentedNearMiss is the half of a detector that
// decides whether anyone keeps it switched on. Each case is material a real
// corpus is full of and that a careless rule reports as a credential.
func TestNoDetectorFiresOnADocumentedNearMiss(t *testing.T) {
	tests := []struct {
		name string
		text string
		// why records what makes the case a near miss rather than a secret.
		why string
	}{
		{
			name: "public key armour block",
			text: "-----BEGIN PUBLIC KEY-----\n" + probePublicKeyBody + "\n-----END PUBLIC KEY-----",
			why:  "PEM armour declares its own content type, and a public key is public by declaration",
		},
		{
			name: "content digest",
			text: "the blob digest was sha256:3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b",
			why:  "content addressing fills the corpus with maximum-entropy hex that is public by purpose",
		},
		{
			name: "git commit",
			text: "reverted at commit 9f1c4d2ba7e603581e2a4b7c6d8e0f1a2b3c4d5e",
			why:  "same as a digest, at a different length",
		},
		{
			name: "uuid",
			text: "the run id was 550e8400-e29b-41d4-a716-446655440000 in the receipt",
			why:  "identifiers are random by design and are meant to be quoted",
		},
		{
			name: "inline image",
			text: "the tool returned data:image/png;base64," + probeInlineImage,
			why:  "a data URI declares that what follows is an encoded payload",
		},
		{
			name: "embedded payload",
			text: "the read returned " + probeEmbeddedPayload,
			why:  "no documented credential format is kilobytes long in one unbroken run",
		},
		{
			name: "long absolute path",
			text: "wrote /home/probe-operator/projects/instrument/internal/preflight/corpus_notes.md",
			why:  "a path is long and mixed but its segments are short and single-class",
		},
		{
			name: "credential file reference",
			text: "ran restic --password-file=/run/probe/keys/repository-password",
			why:  "the value names where a credential lives, not the credential",
		},
		{
			name: "templated credential",
			text: "api_key: ${PROBE_API_KEY}",
			why:  "a template reference is resolved elsewhere",
		},
		{
			name: "documented placeholder",
			text: "call it with Authorization: Bearer YOUR_TOKEN_HERE_PLACEHOLDER",
			why:  "documentation shows the shape on purpose",
		},
		{
			name: "already redacted value",
			text: "password = <redacted>",
			why:  "reporting a redaction as a secret is how a detector loses its reader",
		},
		{
			name: "url without a password",
			text: "postgres://probe-user@catalog.invalid:5432/babel",
			why:  "userinfo without a password is not a credential",
		},
		{
			name: "ssh remote",
			text: "git@github.invalid:probe-operator/instrument.git",
			why:  "a colon before a path is not a URL userinfo separator",
		},
		{
			name: "prose",
			text: "the operator asked whether the verification step had actually run before the claim was made",
			why:  "ordinary text is most of the corpus",
		},
		{
			name: "dotted identifier",
			text: "the trace named internal.preflight.redaction as the failing frame",
			why:  "three dot-separated segments are not a JWT without a header that decodes to JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", test.text)
			rep := mustCheck(t, localRequest(in))
			if secrets := findingsByCategory(rep, CategorySecret); len(secrets) != 0 {
				t.Fatalf("fired on a near miss (%s): %+v", test.why, secrets)
			}
		})
	}
}

// TestNearMissSuppressionIsNotVacuous keeps the case above honest. The dense
// bodies must be reported when their declaring context is removed; otherwise
// the near-miss table would pass simply because the heuristic never fires.
func TestNearMissSuppressionIsNotVacuous(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"public key body alone", "the value was " + probePublicKeyBody},
		{"inline image body alone", "the value was " + probeInlineImage},
		{"payload prefix under the payload floor", "the value was " + probeEmbeddedPayload[:200]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", test.text)
			rep := mustCheck(t, localRequest(in))
			secrets := findingsByCategory(rep, CategorySecret)
			if len(secrets) == 0 {
				t.Fatal("the same bytes are quiet without their context, so the suppression proves nothing")
			}
			for _, f := range secrets {
				if f.Confidence != ConfidenceHeuristic {
					t.Errorf("dense base64 reported as %q rather than a guess", f.Confidence)
				}
			}
		})
	}
}

// TestSecretFindingsAreCountedByConfidence: §5.4's rule that rank is not
// evidence strength has an analogue here. A report that did not separate a
// format match from a guess would let the two be read as the same claim.
func TestSecretFindingsAreCountedByConfidence(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", probeCorpus()...)
	rep := mustCheck(t, localRequest(in))

	if rep.Stats.StructuralSecretFindings == 0 || rep.Stats.HeuristicSecretFindings == 0 {
		t.Fatalf("corpus should produce both kinds of secret finding: %+v", rep.Stats)
	}
	if got, want := rep.Stats.SecretFindings,
		rep.Stats.StructuralSecretFindings+rep.Stats.HeuristicSecretFindings; got != want {
		t.Errorf("secret findings = %d, structural+heuristic = %d", got, want)
	}
	if got := len(findingsByCategory(rep, CategorySecret)); got != rep.Stats.SecretFindings {
		t.Errorf("%d secret findings, stats say %d", got, rep.Stats.SecretFindings)
	}
}

// TestSecretsInArtifactMetadataAreFound: §6.4 names artifact metadata beside
// transcript text, and a path is a real place for a credential to end up —
// a downloaded key, a token saved as a filename.
func TestSecretsInArtifactMetadataAreFound(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", "an ordinary operator message")
	in.Attachments = []Attachment{
		{Path: dir + "/artifacts/notes.md", Size: 1024},
		{Path: dir + "/artifacts/token-" + probeVendorToken + ".txt", Size: 64},
	}
	rep := mustCheck(t, localRequest(in))

	secrets := findingsByCategory(rep, CategorySecret)
	if len(secrets) != 1 {
		t.Fatalf("%d secret findings for one credential-bearing artifact path: %+v", len(secrets), secrets)
	}
	if secrets[0].Detector != "vendor-token" {
		t.Errorf("detector = %q", secrets[0].Detector)
	}
	// With no artifact digest to bind to, the finding is evidenced by the
	// session and names the artifact instead of inventing a digest for it.
	if !strings.Contains(secrets[0].Reference, "token-") {
		t.Errorf("finding does not name the artifact: %q", secrets[0].Reference)
	}
	if secrets[0].Evidence.Locator().Digest != in.Digest {
		t.Errorf("attachment finding is not evidenced by its session: %+v", secrets[0].Evidence.Locator())
	}
	if rep.Stats.Attachments != 2 || rep.Stats.AttachmentBytes != 1088 {
		t.Errorf("attachment stats = %d files, %d bytes", rep.Stats.Attachments, rep.Stats.AttachmentBytes)
	}
}
