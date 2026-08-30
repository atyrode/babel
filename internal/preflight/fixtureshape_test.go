package preflight_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// contiguousCredentialShapes are the formats this repository's push protection
// rejects. They are the same shapes internal/preflight detects, which is the
// whole difficulty: a fixture that exercises a format detector must be in that
// format, and a literal in that format blocks every push carrying the file.
var contiguousCredentialShapes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"AWS access key ID", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA|AIPA|ANPA|AROA)[0-9A-Z]{16}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{20,}\b`)},
}

// TestNoContiguousCredentialShapedLiterals keeps a future fixture from blocking
// everyone's pushes. The failure it prevents is remote and confusing: a push is
// rejected by the forge with no local signal, and the obvious fix — asking the
// forge to allow the secret — is the wrong one, because the value is synthetic
// and the shape is deliberate. Splitting the literal costs one plus sign and
// leaves the assembled constant identical, so the rule is cheap to obey once it
// is visible.
//
// This scans the repository rather than one package because the constraint is
// the repository's, and because the fixtures that need it will keep appearing
// wherever credential handling is tested.
func TestNoContiguousCredentialShapedLiterals(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Skipf("cannot locate the repository root: %v", err)
	}

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, shape := range contiguousCredentialShapes {
			for _, hit := range shape.re.FindAll(data, -1) {
				rel, _ := filepath.Rel(root, path)
				// Report the shape and the location, never the matched
				// bytes: this test's own output would otherwise become the
				// thing it forbids.
				offenders = append(offenders, rel+": "+shape.name+
					" shape, "+itoa(len(hit))+" bytes")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("credential-shaped literals found; assemble them from parts instead "+
			"(see the note in secret_test.go):\n\t%s", strings.Join(offenders, "\n\t"))
	}
}

// repositoryRoot walks up from the test's directory to the module root, so the
// test does not depend on where `go test` was invoked from.
func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fs.ErrNotExist
		}
		dir = parent
	}
}

// itoa avoids importing strconv for one call in a diagnostic.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
