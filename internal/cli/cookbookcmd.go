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
  check    check every recipe's declared version against its content

The cookbook is Babel's versioned analysis guidance (SPEC.md §5): shared
investigation policies, domain lenses, and meta-analysis recipes. It is a
public, reviewable asset compiled into the binary, and it never names a
provider or a model.

Run "babel cookbook <command> -h" for a command's flags.
`

const cookbookListUsage = `Usage: babel cookbook list [flags]

Lists the recipes, their declared versions, the stages they run in, and
whether they are enabled without an operator naming them.

Flags:
  --dir DIR     read this asset tree instead of the embedded one
  --json        emit the listing as JSON on stdout
`

const cookbookCheckUsage = `Usage: babel cookbook check [flags]

Checks §5.1's versioning rule: a recipe whose semantics changed must have
its version increased. Each recipe's content digest is compared against the
committed version record, and any disagreement is reported. Drift exits 1.

This is how the rule stays enforceable from outside the build. A recipe body
edited without a version bump would otherwise leave every receipt claiming a
version whose guidance no longer matches.

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

type cookbookListResult struct {
	Recipes []recipeRow `json:"recipes"`
	Total   int         `json:"total"`
	Source  string      `json:"source"`
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

	res := cookbookListResult{Recipes: rows, Total: len(rows), Source: cookbookSource(*dir)}
	if *asJSON {
		return a.emitJSON(res)
	}
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
		fmt.Fprint(a.stdout, "every recipe's declared version matches its content\n")
	} else {
		table := make([][]string, 0, len(res.Drift))
		for _, d := range res.Drift {
			table = append(table, []string{d.ID, d.Kind, d.Detail})
		}
		if err := writeTable(a.stdout, []string{"RECIPE", "DRIFT", "DETAIL"}, table); err != nil {
			return err
		}
	}
	if res.OK {
		return nil
	}
	// The report is the result document and it has already been written to
	// stdout; the exit code is what a check is for, so the failure is
	// reported without a second explanation.
	a.diagf("%d %s disagree with the version record; increase the version of each changed recipe\n",
		len(res.Drift), plural(len(res.Drift), "recipe", "recipes"))
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
