package sharedcatalog

import (
	"errors"
	"strings"
	"testing"
)

// redactDSN is the last line of defence for SPEC.md §9's rule that errors never
// carry credentials. It is tested in-package because a driver that leaks is not
// something a public API can be made to simulate.
func TestRedactDSNRemovesThePasswordHoweverItAppears(t *testing.T) {
	const password = "s3cr3t-catalog-password"
	dsn := "postgresql://babel_app:" + password + "@db.example.invalid:5432/babel?sslmode=require"

	cases := []struct {
		name string
		err  error
	}{
		{
			// The case the original implementation handled: a verbatim echo.
			name: "whole dsn echoed",
			err:  errors.New("failed to connect to " + dsn),
		},
		{
			// The case it missed. A driver that rebuilds a connection string
			// from parsed fields produces a string that never equals the DSN it
			// was given, so a whole-string replacement cannot match it.
			name: "reconstructed key-value form",
			err:  errors.New("failed to connect: host=db.example.invalid user=babel_app password=" + password + " database=babel"),
		},
		{
			// A password can also surface on its own, for example in an
			// authentication failure that quotes what it tried.
			name: "bare password",
			err:  errors.New("password authentication failed for " + password),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Non-vacuity: the input must actually contain the secret, or an
			// absence in the output would prove nothing.
			if !strings.Contains(tc.err.Error(), password) {
				t.Fatal("the fixture error must contain the password")
			}
			got := redactDSN(tc.err, dsn).Error()
			if strings.Contains(got, password) {
				t.Errorf("the password survived redaction: %s", got)
			}
		})
	}
}

// An empty DSN must not turn every error into a rewritten one, and must not
// redact the empty string into every gap in the message.
func TestRedactDSNWithoutADSNIsIdentity(t *testing.T) {
	original := errors.New("reach shared catalog: timeout")
	if got := redactDSN(original, ""); !errors.Is(got, original) {
		t.Errorf("an absent DSN must pass the error through unchanged, got %v", got)
	}
}
