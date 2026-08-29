package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The catalog table is a browse surface another instance reads, so its columns
// and their meaning are a contract rather than cosmetics. Pinning the exact
// output keeps a reordered column or a renamed header from silently changing
// what an operator - or a cross-host acceptance test - is reading.
func TestPrintCatalogStatusRendersTheCatalogTable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	err := a.printCatalogStatus(&catalogStatus{
		Reachable:    true,
		Uncatalogued: new(0),
		Pending:      new(1),
		Hosts: []catalogHostRow{
			{
				Host: "host-a", Snapshots: 2, Sessions: 3, Pending: 0,
				NewestOrder: 2, NewestSnapshot: "2026-08-28T12:00:00Z",
			},
			{
				Host: "host-b", Snapshots: 1, Sessions: 0, Pending: 1,
				NewestOrder: 1, NewestSnapshot: "2026-08-28T13:30:00Z",
			},
		},
	})
	if err != nil {
		t.Fatalf("printCatalogStatus: %v", err)
	}

	const want = "catalog reachable          yes\n" +
		"uncatalogued snapshots     0\n" +
		"catalog-pending snapshots  1\n" +
		"\ncatalog by host:\n" +
		"HOST    SNAPSHOTS  SESSIONS  PENDING  NEWEST ORDER  NEWEST SNAPSHOT\n" +
		"host-a  2          3         0        2             2026-08-28T12:00:00Z\n" +
		"host-b  1          0         1        1             2026-08-28T13:30:00Z\n"
	if got := stdout.String(); got != want {
		t.Errorf("rendered catalog status:\n%s\nwant:\n%s", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("rendering wrote diagnostics: %q", stderr.String())
	}
}

// An unreachable catalog is reported, not guessed at. The counts must say
// "unknown" rather than 0, and no host table may appear: an empty table would
// claim the fleet is empty when the command never got to look.
func TestPrintCatalogStatusOmitsTheTableWhenItCouldNotLook(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	if err := a.printCatalogStatus(&catalogStatus{}); err != nil {
		t.Fatalf("printCatalogStatus: %v", err)
	}
	const want = "catalog reachable          no\n" +
		"uncatalogued snapshots     unknown\n" +
		"catalog-pending snapshots  unknown\n"
	if got := stdout.String(); got != want {
		t.Errorf("rendered unreachable catalog status:\n%s\nwant:\n%s", got, want)
	}
}

// The JSON is the machine-readable half of the same contract. Absent means
// unknown throughout: an unreachable catalog carries no counts and no hosts,
// and a local-mode run carries no catalog object at all.
func TestStatusResultJSONKeepsUnknownsAbsent(t *testing.T) {
	local, err := json.Marshal(statusResult{Repository: "repo", Snapshots: 0, Hosts: nil})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"repository":"repo","snapshots":0,"hosts":null}`; string(local) != want {
		t.Errorf("local mode JSON = %s, want %s", local, want)
	}

	unreachable, err := json.Marshal(&catalogStatus{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"reachable":false}`; string(unreachable) != want {
		t.Errorf("unreachable catalog JSON = %s, want %s", unreachable, want)
	}

	reachable, err := json.Marshal(&catalogStatus{
		Reachable:    true,
		Uncatalogued: new(0),
		Pending:      new(1),
		Hosts: []catalogHostRow{{
			Host: "host-a", Snapshots: 2, Sessions: 3, Pending: 1,
			NewestOrder: 2, NewestSnapshot: "2026-08-28T12:00:00Z",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"reachable":true,"uncatalogued":0,"pending":1,"hosts":` +
		`[{"host":"host-a","snapshots":2,"sessions":3,"pending":1,` +
		`"newest_order":2,"newest_snapshot":"2026-08-28T12:00:00Z"}]}`
	if string(reachable) != want {
		t.Errorf("catalog JSON = %s, want %s", reachable, want)
	}
}
