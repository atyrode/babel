package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/atyrode/babel/internal/worker"
)

const analysisMigrateUsage = `Usage: babel analysis migrate [--check] [--json]

Remove only a trailing legacy babel mode from configured analysis and title
worker arguments. Keep executable wrappers, other arguments, exact profile
references, metadata and unrelated settings. No profile is selected or created.

Canonical launches resolve every stored reference through Code's offline
engine --describe before any settings are atomically replaced. Import legacy
profiles with Code's engine --import-profiles first if necessary.

Flags:
  --check    read only; exit 1 when migration is needed, without launching the
             legacy worker; canonical launches still resolve profiles offline
  --json     emit {needed, changed, configured}; configured counts profile blocks

No model is called. An unconfigured machine is left untouched.
`

type analysisMigrationResult struct {
	Needed     bool `json:"needed"`
	Changed    bool `json:"changed"`
	Configured int  `json:"configured"`
}

func (a *app) analysisMigrate(ctx context.Context, args []string) error {
	c := newCmd("analysis migrate", analysisMigrateUsage)
	check := c.fs.Bool("check", false, "check without changing settings")
	asJSON := c.fs.Bool("json", false, "emit the migration result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	path, err := analysisPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	res := analysisMigrationResult{}
	if err == nil {
		settings, err := decodeAnalysisSettings(data, path)
		if err != nil {
			return err
		}
		// Raw fields preserve extensions at every level, including profile
		// metadata unknown to this build. Only worker_args is ever replaced.
		var document map[string]json.RawMessage
		if err := json.Unmarshal(data, &document); err != nil {
			return err
		}
		type launch struct {
			name string
			cfg  worker.Config
			ref  worker.ProfileRef
		}
		var launches []launch
		if settings.Profile != nil {
			launches = append(launches, launch{"analysis", worker.Config{Binary: settings.Worker, Args: settings.WorkerArgs}, settings.Profile.ref()})
		}
		if settings.Titles != nil {
			launches = append(launches, launch{"titles", settings.Titles.config(), settings.Titles.ref()})
		}
		res.Configured = len(launches)
		for i := range launches {
			l := &launches[i]
			argv := l.cfg.Args
			if len(argv) != 0 && argv[len(argv)-1] == legacyWorkerSubcommand {
				l.cfg.Args = argv[:len(argv)-1]
				res.Needed = true
				fields := document
				if l.name == "titles" {
					fields = nil
					if err := json.Unmarshal(document["titles"], &fields); err != nil {
						return err
					}
				}
				encoded, err := json.Marshal(l.cfg.Args)
				if err != nil {
					return err
				}
				fields["worker_args"] = encoded
				if l.name == "titles" {
					encoded, err := json.Marshal(fields)
					if err != nil {
						return err
					}
					document["titles"] = encoded
				}
			}
			if err := refuseDials(c, l.cfg.Args, true); err != nil {
				return err
			}
		}
		// A pending check must work before the new Code/profile store is
		// installed, and must never start an old worker to discover that fact.
		if !*check || !res.Needed {
			for _, l := range launches {
				if _, err := describeProfile(ctx, l.cfg, l.ref); err != nil {
					return fmt.Errorf("%s profile %s: offline migration validation failed; settings unchanged: %w", l.name, l.ref.String(), err)
				}
			}
			if res.Needed {
				encoded, err := json.MarshalIndent(document, "", "  ")
				if err != nil {
					return err
				}
				if _, err := saveAnalysisDocument(path, append(encoded, '\n')); err != nil {
					return err
				}
				res.Changed = true
			}
		}
	}
	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.stdout, "analysis launch migration: needed=%t changed=%t configured=%d\n", res.Needed, res.Changed, res.Configured)
	}
	if *check && res.Needed {
		return errReported
	}
	return nil
}
