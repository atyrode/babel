package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// fallbackVersion is reported when the binary carries no module version,
// which is the normal case for a `go build` or `go run` binary.
const fallbackVersion = "devel"

// shortCommitLen bounds the displayed VCS revision.
const shortCommitLen = 12

// buildVersion, buildCommit and buildTime are injected at link time with
// `-ldflags -X` by builds that compile outside a VCS checkout, where
// `-buildvcs` can stamp nothing: the Nix derivation builds from a source
// copy with no `.git`, so build info alone would report no provenance at
// all. An injected value wins over build info; with nothing injected the
// reported identity is exactly what build info carries.
var (
	buildVersion string
	buildCommit  string
	buildTime    string
)

// dirtyRevSuffix marks an injected revision taken from a modified working
// tree (Nix's `dirtyRev`): the injected equivalent of `vcs.modified=true`.
const dirtyRevSuffix = "-dirty"

// unknownRev is the placeholder a builder injects when it has no revision to
// report, such as a flake evaluated from a tree carrying no git metadata. It
// is deliberately not recorded as a commit: an empty `commit` field says
// "no provenance" honestly, where the literal string would masquerade as one.
const unknownRev = "unknown"

const versionUsage = `Usage: babel version [--json]

Print Babel's build identity: module version, VCS revision and dirty state
when the build recorded them, Go version, and platform.

Flags:
  --json    emit the result as a JSON object on stdout
`

// buildIdentity is Babel's build provenance, as recorded in commit records
// and reported by `babel version`.
type buildIdentity struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Dirty     bool   `json:"dirty"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// readBuildIdentity resolves the build identity from the embedded build
// info. A binary built without module or VCS stamping still reports a
// usable value: provenance degrades explicitly instead of failing.
func readBuildIdentity() buildIdentity {
	id := buildIdentity{
		Version:   fallbackVersion,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			id.Version = v
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				id.Commit = s.Value
			case "vcs.modified":
				id.Dirty = s.Value == "true"
			case "vcs.time":
				id.BuildTime = s.Value
			}
		}
	}
	id.applyInjected()
	return id
}

// applyInjected overlays the link-time values onto the build info result.
// An injected revision comes from a source snapshot whose cleanliness the
// builder already resolved, so it settles the dirty flag too: dirty exactly
// when the revision itself carries the marker. A build with an unknown
// revision reports clean, because there is no tree left to have modified.
func (id *buildIdentity) applyInjected() {
	if buildVersion != "" {
		id.Version = buildVersion
	}
	if buildCommit != "" && buildCommit != unknownRev {
		id.Commit = strings.TrimSuffix(buildCommit, dirtyRevSuffix)
		id.Dirty = strings.HasSuffix(buildCommit, dirtyRevSuffix)
	}
	if buildTime != "" {
		id.BuildTime = buildTime
	}
}

// String renders the human one-line form.
func (id buildIdentity) String() string {
	s := "babel " + id.Version
	switch {
	case id.Commit != "":
		s += " (" + id.shortCommit()
		if id.Dirty {
			s += ", dirty"
		}
		s += ")"
	case id.Dirty:
		s += " (dirty)"
	}
	return s + " " + id.Platform + " " + id.GoVersion
}

func (id buildIdentity) shortCommit() string {
	if len(id.Commit) > shortCommitLen {
		return id.Commit[:shortCommitLen]
	}
	return id.Commit
}

// provenance is the compact version recorded in commit records
// (archive.CommitRecord.BabelVersion), which must identify the exact build
// that published a generation. A module version that already encodes the
// revision or dirty state is not annotated twice.
func (id buildIdentity) provenance() string {
	s := id.Version
	if id.Commit != "" && !strings.Contains(s, id.shortCommit()) {
		s += "+" + id.shortCommit()
	}
	if id.Dirty && !strings.HasSuffix(s, "+dirty") {
		s += "+dirty"
	}
	return s
}

// version implements `babel version`.
func (a *app) version(args []string) error {
	c := newCmd("version", versionUsage)
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	id := readBuildIdentity()
	if *asJSON {
		return a.emitJSON(id)
	}
	fmt.Fprintln(a.stdout, id.String())
	return nil
}
