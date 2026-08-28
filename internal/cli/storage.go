package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/atyrode/babel/internal/config"
)

const storageUsage = `Usage: babel storage <command> [flags]

Commands:
  configure --from-json FILE|-   replace persistent storage configuration
  status                         report persistent storage configuration

Run "babel storage <command> -h" for a command's flags.
`

const storageConfigureUsage = `Usage: babel storage configure --from-json FILE|- [--json]

Reads a complete storage configuration from FILE, or from stdin when FILE is
"-", validates it, and atomically replaces the whole storage.json file.

Flags:
  --from-json FILE|-          complete JSON configuration to install (required)
  --json                      emit {path, repository, host_id} as JSON
`

const storageStatusUsage = `Usage: babel storage status [--json]

Reports the persistent storage configuration and checks the password file's
existence and permissions. This command succeeds when storage is unconfigured.

Flags:
  --json                      emit the report as JSON
`

// storage routes `babel storage <verb>`.
func (a *app) storage(args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "storage requires a subcommand", usage: storageUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, storageUsage)
		return nil
	case "configure":
		return a.storageConfigure(args[1:])
	case "status":
		return a.storageStatus(args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown storage subcommand %q", args[0]), usage: storageUsage}
	}
}

type storageConfigureResult struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
	HostID     string `json:"host_id"`
}

func (a *app) storageConfigure(args []string) error {
	c := newCmd("storage configure", storageConfigureUsage)
	fromJSON := c.fs.String("from-json", "", "complete JSON configuration to install")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *fromJSON == "" {
		return c.usagef("storage configure requires --from-json FILE|-")
	}

	in := a.stdin
	var closeInput func() error
	if *fromJSON != "-" {
		f, err := os.Open(*fromJSON)
		if err != nil {
			return fmt.Errorf("open configuration input %s: %w", *fromJSON, err)
		}
		in = f
		closeInput = f.Close
	}

	var cfg config.Config
	dec := json.NewDecoder(in)
	if err := dec.Decode(&cfg); err != nil {
		if closeInput != nil {
			_ = closeInput()
		}
		return fmt.Errorf("decode configuration input: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if closeInput != nil {
			_ = closeInput()
		}
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode configuration input: %w", err)
	}
	if closeInput != nil {
		if err := closeInput(); err != nil {
			return fmt.Errorf("close configuration input: %w", err)
		}
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	res := storageConfigureResult{
		Path:       Sanitize(config.Path()),
		Repository: Sanitize(cfg.Repository),
		HostID:     Sanitize(cfg.HostID),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "storage configuration written to %s\n", res.Path)
	return nil
}

type storageStatusResult struct {
	Path               string `json:"path"`
	Exists             bool   `json:"exists"`
	Repository         string `json:"repository"`
	PasswordFile       string `json:"password_file"`
	PasswordFileExists bool   `json:"password_file_exists"`
	PasswordFileSecure bool   `json:"password_file_secure"`
	HostID             string `json:"host_id"`
	ResticBinary       string `json:"restic_binary"`
}

func (a *app) storageStatus(args []string) error {
	c := newCmd("storage status", storageStatusUsage)
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	cfg, found, err := config.Load()
	if err != nil {
		return err
	}
	res := storageStatusResult{
		Path:         Sanitize(config.Path()),
		Exists:       found,
		Repository:   Sanitize(cfg.Repository),
		PasswordFile: Sanitize(cfg.PasswordFile),
		HostID:       Sanitize(cfg.HostID),
		ResticBinary: Sanitize(cfg.ResticBinary),
	}
	if cfg.PasswordFile != "" {
		info, statErr := os.Stat(cfg.PasswordFile)
		switch {
		case statErr == nil:
			res.PasswordFileExists = true
			res.PasswordFileSecure = info.Mode().Perm()&^os.FileMode(0o600) == 0
			if !res.PasswordFileSecure {
				a.diagf("warning: password file %s has permissions %04o; expected 0600 or stricter\n", Sanitize(cfg.PasswordFile), info.Mode().Perm())
			}
		case errors.Is(statErr, os.ErrNotExist):
			a.diagf("warning: password file %s does not exist\n", Sanitize(cfg.PasswordFile))
		default:
			return fmt.Errorf("inspect password file %s: %w", cfg.PasswordFile, statErr)
		}
	}

	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"path", res.Path},
		{"configured", yesNo(res.Exists, "yes", "no")},
		{"repository", res.Repository},
		{"password file", res.PasswordFile},
		{"password file exists", yesNo(res.PasswordFileExists, "yes", "no")},
		{"password file secure", yesNo(res.PasswordFileSecure, "yes", "no")},
		{"host id", res.HostID},
		{"restic binary", res.ResticBinary},
	}
	return writeDetail(a.stdout, rows)
}
