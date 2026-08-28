package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Repository:   filepath.Join(t.TempDir(), "repo"),
		PasswordFile: filepath.Join(t.TempDir(), "password"),
		HostID:       "host-1",
		ResticBinary: "/opt/restic",
	}
}

func TestLoadMissingAndSaveRoundTrip(t *testing.T) {
	home := configHome(t)
	if cfg, found, err := Load(); err != nil || found || cfg != (Config{}) {
		t.Fatalf("Load missing = (%+v, %v, %v)", cfg, found, err)
	}
	if got, want := Path(), filepath.Join(home, "babel", "storage.json"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}

	want := validConfig(t)
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("storage.json mode = %04o, want 0600", got)
	}
	if got := mustStat(t, filepath.Dir(Path())).Mode().Perm(); got != 0o700 {
		t.Fatalf("configuration directory mode = %04o, want 0700", got)
	}

	got, found, err := Load()
	if err != nil || !found {
		t.Fatalf("Load saved = (%+v, %v, %v)", got, found, err)
	}
	want.ConfigSchema = 1
	if got != want {
		t.Fatalf("Load saved = %+v, want %+v", got, want)
	}
}

func TestLoadCompatibilityAndMalformedInput(t *testing.T) {
	configHome(t)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"config_schema":1,"repository":"repo","password_file":"/password","future_field":{"kept":false}}`)
	cfg, found, err := Load()
	if err != nil || !found || cfg.Repository != "repo" {
		t.Fatalf("Load with unknown field = (%+v, %v, %v)", cfg, found, err)
	}

	write(`{"config_schema":2,"repository":"repo","password_file":"/password"}`)
	if _, _, err := Load(); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Load future schema error = %v", err)
	}

	for _, body := range []string{`{`, `{}` + `{}`} {
		write(body)
		if _, _, err := Load(); err == nil {
			t.Fatalf("Load malformed %q succeeded", body)
		}
	}
}

func TestSaveValidationDoesNotReplaceExistingFile(t *testing.T) {
	configHome(t)
	good := validConfig(t)
	if err := Save(good); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}

	cases := []Config{
		{PasswordFile: good.PasswordFile},
		{Repository: good.Repository},
		{Repository: good.Repository, PasswordFile: "relative"},
		{Repository: good.Repository, PasswordFile: good.PasswordFile, HostID: "Bad Host"},
		{ConfigSchema: 2, Repository: good.Repository, PasswordFile: good.PasswordFile},
	}
	for _, cfg := range cases {
		if err := Save(cfg); err == nil {
			t.Fatalf("Save(%+v) succeeded", cfg)
		}
		after, err := os.ReadFile(Path())
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("invalid Save replaced existing configuration")
		}
	}
}

func TestValidHostID(t *testing.T) {
	for _, value := range []string{"host", "host-1", "1.example_host"} {
		if !ValidHostID(value) {
			t.Errorf("ValidHostID(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-host", "Host", "two words", strings.Repeat("a", 65)} {
		if ValidHostID(value) {
			t.Errorf("ValidHostID(%q) = true", value)
		}
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
