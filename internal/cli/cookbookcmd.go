package cli

import (
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	cookbookassets "github.com/atyrode/babel/cookbook"
	"github.com/atyrode/babel/internal/cookbook"
)

const cookbookUsage = `Usage: babel cookbook <command> [flags]

Commands:
  list     list the analysis recipes this build carries
  check    check every document's declared version against its content

The cookbook is Babel's versioned analysis guidance (SPEC.md §5): shared
investigation policies, domain lenses, and meta-analysis recipes, opening
with the statement they are written and reviewed against. It is a public,
reviewable asset compiled into the binary, and it never names a provider or
a model.

Run "babel cookbook <command> -h" for a command's flags.
`

const cookbookListUsage = `Usage: babel cookbook list [flags]

Names the cookbook's standing statement, then lists the recipes, their
declared versions, the stages they run in, and whether they are enabled
without an operator naming them.

Flags:
  --dir DIR     read this asset tree instead of the embedded one
  --json        emit the listing as JSON on stdout
`

const cookbookCheckUsage = `Usage: babel cookbook check [flags]

Checks §5.1's versioning rule: a document whose semantics changed must have
its version increased. The content digest of the cookbook's statement and of
each recipe is compared against the committed version record, and any
disagreement is reported. Drift exits 1.

This is how the rule stays enforceable from outside the build. A recipe body
edited without a version bump would otherwise leave every receipt claiming a
version whose guidance no longer matches, and a silently restated preamble
would leave every review citing a standard that is gone.

Flags:
  --dir DIR     check this asset tree instead of the embedded one
  --json        emit the report as JSON on stdout
`

// recipeRow is one recipe in machine-readable output.
type recipeRow struct {
	ID           string   `json:"id"`
	Version      int      `json:"version"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Path         string   `json:"path"`
	Default      bool     `json:"default"`
	Scope        []string `json:"scope"`
	Stages       []string `json:"stages"`
	Capabilities []string `json:"capabilities,omitempty"`
	Digest       string   `json:"digest"`
}

// preambleRow is the cookbook's standing statement in machine-readable
// output. The text itself is not emitted: it is a page of prose meant to be
// read in the repository or the diff that changes it, and what a listing
// needs to say is that it exists, which version the recipes were written
// under, and where to read it.
type preambleRow struct {
	Version int    `json:"version"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
}

type cookbookListResult struct {
	Preamble preambleRow `json:"preamble"`
	Recipes  []recipeRow `json:"recipes"`
	Total    int         `json:"total"`
	Source   string      `json:"source"`
}

type driftRow struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type cookbookCheckResult struct {
	OK     bool       `json:"ok"`
	Drift  []driftRow `json:"drift"`
	Source string     `json:"source"`
}

// cookbookCmd routes `babel cookbook <verb>`.
func (a *app) cookbookCmd(args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "cookbook requires a subcommand", usage: cookbookUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, cookbookUsage)
		return nil
	case "list":
		return a.cookbookList(args[1:])
	case "check":
		return a.cookbookCheck(args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown cookbook subcommand %q", args[0]), usage: cookbookUsage}
	}
}

func (a *app) cookbookList(args []string) error {
	c := newCmd("cookbook list", cookbookListUsage)
	dir := c.fs.String("dir", "", "read this asset tree instead of the embedded one")
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	set, err := cookbook.Load(cookbookTree(*dir))
	if err != nil {
		return err
	}

	recipes := set.All()
	rows := make([]recipeRow, 0, len(recipes))
	for _, r := range recipes {
		row := recipeRow{
			ID:      Sanitize(r.ID),
			Version: r.Version,
			Kind:    Sanitize(string(r.Kind)),
			Title:   Sanitize(r.Title),
			Path:    Sanitize(r.Path),
			Default: r.Default,
			Digest:  string(r.Digest),
		}
		for _, s := range r.Scope {
			row.Scope = append(row.Scope, Sanitize(string(s)))
		}
		for _, s := range r.Stages {
			row.Stages = append(row.Stages, Sanitize(string(s)))
		}
		for _, cap := range r.Capabilities {
			row.Capabilities = append(row.Capabilities, Sanitize(string(cap)))
		}
		rows = append(rows, row)
	}

	preamble := set.Preamble()
	res := cookbookListResult{
		Preamble: preambleRow{
			Version: preamble.Version,
			Title:   Sanitize(preamble.Title),
			Path:    Sanitize(preamble.Path),
			Digest:  string(preamble.Digest),
		},
		Recipes: rows,
		Total:   len(rows),
		Source:  cookbookSource(*dir),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	// The statement is named above the recipes because that is its relation
	// to them: it is what they are written and reviewed against, and a
	// listing that only showed the table would present the lenses as peers
	// with nothing above them.
	fmt.Fprintf(a.stdout, "%s (%s, version %d)\n\n", res.Preamble.Title, res.Preamble.Path, res.Preamble.Version)
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.ID, strconv.Itoa(row.Version), row.Kind,
			yesNo(row.Default, "yes", "no"), strings.Join(row.Stages, ","), row.Title})
	}
	return writeTable(a.stdout, []string{"ID", "VERSION", "KIND", "DEFAULT", "STAGES", "TITLE"}, table)
}

func (a *app) cookbookCheck(args []string) error {
	c := newCmd("cookbook check", cookbookCheckUsage)
	dir := c.fs.String("dir", "", "check this asset tree instead of the embedded one")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	drift, err := cookbook.Verify(cookbookTree(*dir))
	if err != nil {
		return err
	}

	res := cookbookCheckResult{OK: len(drift) == 0, Drift: make([]driftRow, 0, len(drift)), Source: cookbookSource(*dir)}
	for _, d := range drift {
		res.Drift = append(res.Drift, driftRow{
			ID:     Sanitize(d.ID),
			Kind:   Sanitize(string(d.Kind)),
			Detail: Sanitize(d.Detail),
		})
	}
	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else if res.OK {
		fmt.Fprint(a.stdout, "the statement and every recipe declare a version that matches their content\n")
	} else {
		table := make([][]string, 0, len(res.Drift))
		for _, d := range res.Drift {
			table = append(table, []string{d.ID, d.Kind, d.Detail})
		}
		if err := writeTable(a.stdout, []string{"DOCUMENT", "DRIFT", "DETAIL"}, table); err != nil {
			return err
		}
	}
	if res.OK {
		return nil
	}
	// The report is the result document and it has already been written to
	// stdout; the exit code is what a check is for, so the failure is
	// reported without a second explanation.
	a.diagf("%d cookbook %s %s with the version record; increase the version of each changed document\n",
		len(res.Drift), plural(len(res.Drift), "document", "documents"),
		plural(len(res.Drift), "disagrees", "disagree"))
	return errReported
}

// cookbookTree resolves which asset tree a command reads. The embedded tree
// is what a run actually applies; --dir is for checking a working copy
// before it is committed, which is the moment §5.1's rule is cheapest to
// enforce.
func cookbookTree(dir string) fs.FS {
	if dir == "" {
		return cookbookassets.Assets()
	}
	return os.DirFS(dir)
}

func cookbookSource(dir string) string {
	if dir == "" {
		return "embedded"
	}
	return Sanitize(dir)
}
