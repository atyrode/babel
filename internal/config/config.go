// Package config owns Babel's persistent storage configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	currentSchema = 1
	maxHostIDLen  = 64
)

// Config is the complete persistent repository selection stored in storage.json.
type Config struct {
	ConfigSchema int    `json:"config_schema"`
	Repository   string `json:"repository"`
	PasswordFile string `json:"password_file"`
	HostID       string `json:"host_id,omitempty"`
	ResticBinary string `json:"restic_binary,omitempty"`
}

// Path returns the location of Babel's persistent storage configuration.
func Path() string {
	path, _ := pathName()
	return path
}

// Load reads storage.json. A missing file is not an error. Unknown fields are
// ignored so an older Babel can read configuration written by a compatible
// newer version.
func Load() (Config, bool, error) {
	path, err := pathName()
	if err != nil {
		return Config{}, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("open storage configuration %s: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	dec := json.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, false, fmt.Errorf("decode storage configuration %s: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Config{}, false, fmt.Errorf("decode storage configuration %s: %w", path, err)
	}
	if cfg.ConfigSchema > currentSchema {
		return Config{}, false, fmt.Errorf("storage configuration schema %d is newer than supported schema %d", cfg.ConfigSchema, currentSchema)
	}
	return cfg, true, nil
}

// Save validates and atomically replaces storage.json. The containing
// directory and file are private to the current user.
func Save(cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	cfg.ConfigSchema = currentSchema

	path, err := pathName()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, ".storage.json-*")
	if err != nil {
		return fmt.Errorf("create temporary storage configuration: %w", err)
	}
	temp := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temp)
		}
	}()

	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary storage configuration: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode storage configuration: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync storage configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close storage configuration: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace storage configuration %s: %w", path, err)
	}
	keep = true
	return nil
}

// Validate checks the complete configuration accepted by Save and by
// `babel storage configure`.
func Validate(cfg Config) error {
	if cfg.ConfigSchema > currentSchema {
		return fmt.Errorf("storage configuration schema %d is newer than supported schema %d", cfg.ConfigSchema, currentSchema)
	}
	if cfg.Repository == "" {
		return errors.New("storage configuration repository is required")
	}
	if cfg.PasswordFile == "" {
		return errors.New("storage configuration password_file is required")
	}
	if !filepath.IsAbs(cfg.PasswordFile) {
		return errors.New("storage configuration password_file must be an absolute path")
	}
	if cfg.HostID != "" && !ValidHostID(cfg.HostID) {
		return fmt.Errorf("storage configuration host_id %q is invalid: host ids are 1-%d characters of [a-z0-9._-] starting alphanumeric", cfg.HostID, maxHostIDLen)
	}
	return nil
}

// ValidHostID reports whether s is 1-64 characters of [a-z0-9._-] and
// starts with an alphanumeric character.
func ValidHostID(s string) bool {
	if s == "" || len(s) > maxHostIDLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

func pathName() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve configuration directory: %w", err)
	}
	return filepath.Join(base, "babel", "storage.json"), nil
}
