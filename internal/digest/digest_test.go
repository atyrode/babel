package digest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/digest"
)

// These vectors are the point of the package. A content-addressed store is
// only content-addressed if two independent implementations agree on the name
// of a byte string: OMP names blob files by their hash, and Babel verifies
// them by recomputing it. The expected values below are plain SHA-256 sums, so
// an operator can confirm any of them with `printf %s abc | sha256sum` without
// running Go at all. Written as literals rather than as sha256.Sum256 calls on
// purpose - deriving the expectation from the same primitive the code uses
// would make a swapped hash function agree with itself.
func TestKnownAnswersMatchPlainSha256(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want digest.Digest
	}{
		{
			name: "empty",
			in:   "",
			want: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "abc",
			in:   "abc",
			want: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		},
		{
			name: "babel",
			in:   "babel",
			want: "sha256:9a8c37a8a805f5ec0fef0237831d63a273e02afb7e88f72e743d9a4097e393aa",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := digest.Bytes([]byte(tc.in)); got != tc.want {
				t.Errorf("Bytes(%q) = %q, want %q.\nThe canonical digest is a "+
					"plain SHA-256 sum; a mismatch means the hash function or "+
					"the hex encoding changed, and every stored digest in the "+
					"fleet now names different bytes.", tc.in, got, tc.want)
			}

			// The three entry points are three spellings of one answer. A
			// caller that streams a file must get the same name as a caller
			// that holds the bytes, or verification depends on which door the
			// content came through.
			got, n, err := digest.Compute(strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("Compute(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Compute(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if n != int64(len(tc.in)) {
				t.Errorf("Compute(%q) size = %d, want %d", tc.in, n, len(tc.in))
			}
			if got := digest.New(sha256.Sum256([]byte(tc.in))); got != tc.want {
				t.Errorf("New(sha256.Sum256(%q)) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Compute is given readers it does not own - open files, network streams - and
// a read failure part way through must not be reported as a digest. Returning
// a partial hash would be worse than an error: it would name bytes nobody has.
func TestComputeReturnsNoDigestOnReadFailure(t *testing.T) {
	boom := errors.New("disk went away")
	got, n, err := digest.Compute(io.MultiReader(
		strings.NewReader("first half"),
		failingReader{err: boom},
	))
	if !errors.Is(err, boom) {
		t.Fatalf("Compute error = %v, want %v", err, boom)
	}
	if got != "" {
		t.Errorf("Compute returned digest %q alongside an error; a partial "+
			"hash names bytes that do not exist", got)
	}
	if n != 0 {
		t.Errorf("Compute returned size %d alongside an error, want 0", n)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// The canonical form is a wire and filesystem contract, not a display choice:
// digests are compared as strings, embedded in metadata JSON, and used as
// directory names. This asserts the shape byte for byte - the exact prefix,
// the exact hex length, lowercase only - and that hex decoding recovers the
// sum the digest was built from.
func TestCanonicalFormIsLowercaseHexBehindTheSha256Prefix(t *testing.T) {
	for _, in := range []string{"", "abc", strings.Repeat("payload\n", 1024)} {
		sum := sha256.Sum256([]byte(in))
		d := digest.New(sum)
		s := string(d)

		if !strings.HasPrefix(s, "sha256:") {
			t.Fatalf("digest %q does not start with the sha256: prefix", s)
		}
		if len(s) != len("sha256:")+64 {
			t.Fatalf("digest %q is %d characters, want %d", s, len(s), len("sha256:")+64)
		}
		if got := d.Hex(); len(got) != 64 {
			t.Fatalf("Hex() = %q, want 64 characters", got)
		}
		if got := d.Hex(); got != strings.ToLower(got) {
			t.Errorf("Hex() = %q, want lowercase; a digest that differs only in "+
				"case is a different string to every consumer that compares it", got)
		}
		raw, err := hex.DecodeString(d.Hex())
		if err != nil {
			t.Fatalf("Hex() = %q is not hex: %v", d.Hex(), err)
		}
		if !bytes.Equal(raw, sum[:]) {
			t.Errorf("Hex() decodes to %x, want %x", raw, sum[:])
		}

		// Round trip: what the package formats, the package must accept.
		// A formatter and a validator that disagree would reject digests the
		// same binary wrote a moment earlier.
		if !d.Valid() {
			t.Errorf("Valid() rejected %q, which New produced", s)
		}
	}
}

// Valid is the gate every stored digest passes before it is trusted, so what
// it refuses matters more than what it accepts. Each case here is a shape that
// has a real way of arriving: hand-edited metadata, a sum copied from a tool
// that emits uppercase, a truncated field, another algorithm's output, or a
// value assembled by concatenating a prefix onto something that already had
// one.
func TestValidRejectsEveryNonCanonicalShape(t *testing.T) {
	const good = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	cases := []struct {
		name string
		in   digest.Digest
		why  string
	}{
		{"empty", "", "the zero value must never read as a digest"},
		{"prefix only", "sha256:", "a prefix names nothing"},
		{"bare hex", digest.Digest(good), "the algorithm is part of the name"},
		{"wrong algorithm", digest.Digest("sha512:" + good), "sha512: is not sha256:"},
		{"uppercase prefix", digest.Digest("SHA256:" + good), "the prefix is lowercase"},
		{"uppercase hex", digest.Digest("sha256:" + strings.ToUpper(good)), "hex is lowercase"},
		{"mixed case hex", digest.Digest("sha256:E3b0" + good[4:]), "hex is lowercase"},
		{"truncated", digest.Digest("sha256:" + good[:63]), "63 hex characters is not a sha256 sum"},
		{"overlong", digest.Digest("sha256:" + good + "a"), "65 hex characters is not a sha256 sum"},
		{"non-hex payload", digest.Digest("sha256:" + "g" + good[1:]), "g is not a hex digit"},
		{"leading space", digest.Digest(" sha256:" + good), "no surrounding whitespace"},
		{"trailing newline", digest.Digest("sha256:" + good + "\n"), "no surrounding whitespace"},
		{"sha256sum output", digest.Digest("sha256:" + good + "  -"), "the filename column is not part of the digest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.in.Valid() {
				t.Errorf("Valid() accepted %q: %s", tc.in, tc.why)
			}
		})
	}
}

// Hex trims the prefix, and TrimPrefix removes one occurrence, not all of
// them. A value whose payload itself begins with "sha256:" therefore starts
// with the right prefix and still is not a digest, so Valid - not the trim -
// has to be what stops it. This pins that: the doubled form is the correct
// total length, which is exactly what makes a length-only check insufficient.
func TestPrefixTrimmingCannotBeFooledByADoubledPrefix(t *testing.T) {
	const good = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	doubled := digest.Digest("sha256:sha256:" + good[len("sha256:"):])
	if len(string(doubled)) != len("sha256:")+64 {
		t.Fatalf("test vector is %d characters, want %d; it must be the right "+
			"length or it proves nothing about a length-only check",
			len(string(doubled)), len("sha256:")+64)
	}
	if !strings.HasPrefix(string(doubled), "sha256:") {
		t.Fatal("test vector must start with the canonical prefix")
	}
	if doubled.Valid() {
		t.Errorf("Valid() accepted %q; trimming the prefix leaves %q, which is "+
			"not 64 hex characters", doubled, doubled.Hex())
	}

	// And the honest case still works: Hex strips exactly the prefix, leaving
	// the payload untouched.
	d := digest.Digest("sha256:" + good)
	if got := d.Hex(); got != good {
		t.Errorf("Hex() = %q, want %q", got, good)
	}
}
