package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
)

func TestStorageConfigureFromStdinDrivesArchiveStatus(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))

	// Create a real repository first, then remove every flag/environment
	// selection so status has only storage.json to resolve from.
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)
	t.Setenv("BABEL_HOST_ID", "")
	body, err := json.Marshal(config.Config{
		ConfigSchema: 1,
		Repository:   f.repoDir,
		PasswordFile: f.passwordFile,
		HostID:       "configured-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"storage", "configure", "--from-json", "-", "--json"}, bytes.NewReader(body), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("configure via stdin exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	configured := decode[storageConfigureResult](t, stdout.String())
	if configured.Path != config.Path() || configured.Repository != f.repoDir || configured.HostID != "configured-host" {
		t.Fatalf("configure result = %+v", configured)
	}
	if stderr.Len() != 0 {
		t.Fatalf("configure diagnostics = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"archive", "status", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("archive status through storage.json exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	status := decode[statusResult](t, stdout.String())
	if status.Repository != f.repoDir || status.Snapshots != 1 {
		t.Fatalf("archive status through storage.json = %+v", status)
	}
}

func TestRepositorySelectionPrecedence(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	t.Setenv("BABEL_HOST_ID", "")
	cfg := config.Config{
		Repository:   filepath.Join(f.root, "config-repo"),
		PasswordFile: filepath.Join(f.root, "config-password"),
		HostID:       "config-host",
		ResticBinary: "/config/restic",
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	open := func(rf *repoFlags) {
		t.Helper()
		c := newCmd("test", "")
		if _, err := rf.open(c, dirs{cache: filepath.Join(f.root, "restic-cache")}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	var fromConfig repoFlags
	open(&fromConfig)
	if fromConfig.repository != cfg.Repository || fromConfig.passwordFile != cfg.PasswordFile || fromConfig.binary != cfg.ResticBinary {
		t.Fatalf("config selection = %+v", fromConfig)
	}
	if got, err := fromConfig.hostID(newCmd("test", "")); err != nil || got != cfg.HostID {
		t.Fatalf("config host = (%q, %v)", got, err)
	}

	envRepo := filepath.Join(f.root, "env-repo")
	envPassword := filepath.Join(f.root, "env-password")
	t.Setenv("BABEL_RESTIC_REPO", envRepo)
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", envPassword)
	t.Setenv("BABEL_HOST_ID", "env-host")
	var fromEnv repoFlags
	open(&fromEnv)
	if fromEnv.repository != envRepo || fromEnv.passwordFile != envPassword {
		t.Fatalf("environment selection = %+v", fromEnv)
	}
	if got, err := fromEnv.hostID(newCmd("test", "")); err != nil || got != "env-host" {
		t.Fatalf("environment host = (%q, %v)", got, err)
	}

	fromFlags := repoFlags{
		repository:   filepath.Join(f.root, "flag-repo"),
		passwordFile: filepath.Join(f.root, "flag-password"),
		binary:       "/flag/restic",
		host:         "flag-host",
	}
	open(&fromFlags)
	if fromFlags.repository != filepath.Join(f.root, "flag-repo") || fromFlags.passwordFile != filepath.Join(f.root, "flag-password") || fromFlags.binary != "/flag/restic" {
		t.Fatalf("flag selection = %+v", fromFlags)
	}
	if got, err := fromFlags.hostID(newCmd("test", "")); err != nil || got != "flag-host" {
		t.Fatalf("flag host = (%q, %v)", got, err)
	}
}

func TestStorageStatusMissingAndPasswordPermissions(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))

	stdout, stderr := f.ok("storage", "status", "--json")
	missing := decode[storageStatusResult](t, stdout)
	if missing.Exists || missing.Path != config.Path() || stderr != "" {
		t.Fatalf("unconfigured status = %+v, stderr %q", missing, stderr)
	}

	password := filepath.Join(f.root, "password-status")
	if err := os.WriteFile(password, []byte("synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The mode is asserted, so it must be set rather than requested: a
	// developer or CI runner with umask 0077 would otherwise get 0600 here and
	// see this case pass for the wrong reason.
	if err := os.Chmod(password, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.Config{Repository: "repo", PasswordFile: password}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr = f.ok("storage", "status", "--json")
	status := decode[storageStatusResult](t, stdout)
	if !status.Exists || !status.PasswordFileExists || status.PasswordFileSecure {
		t.Fatalf("insecure password status = %+v", status)
	}
	if !strings.Contains(stderr, "expected 0600 or stricter") {
		t.Fatalf("insecure password warning = %q", stderr)
	}
}

func TestStorageConfigureRejectsInvalidInputWithoutReplacement(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	good := config.Config{Repository: "repo", PasswordFile: filepath.Join(f.root, "password")}
	if err := config.Save(good); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"storage", "configure", "--from-json", "-"}, strings.NewReader(`{"repository":"replacement","password_file":"relative"}`), &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("invalid configure exited %d, stderr %q", code, stderr.String())
	}
	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("invalid configure replaced storage.json")
	}
}

func TestStorageDispatchUsage(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.ok("--help")
	if !strings.Contains(stdout, "storage configure") || stderr != "" {
		t.Fatalf("root help omitted storage commands: stdout %q stderr %q", stdout, stderr)
	}
}

// `storage configure` is the first command anyone runs, and its input is a
// whole JSON document with no flags to discover it from. The property
// defended here is that its usage is an interface rather than an invitation
// to guess a field name per error: it names what is required and what is
// optional, and the document it prints is one this build actually accepts.
// The example is extracted and run through the real validator rather than
// eyeballed, so a schema change that invalidates the documentation fails
// here instead of in an operator's terminal.
func TestStorageConfigureUsageDocumentsAWorkingSchema(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.ok("storage", "configure", "-h")
	if stderr != "" {
		t.Fatalf("configure help wrote diagnostics: %q", stderr)
	}
	// Required first, then the optional names an operator would otherwise
	// have to discover from internal/config's struct tags.
	for _, field := range []string{
		"repository", "password_file", "config_schema", "mode", "host_id",
		"restic_binary", "repository_store", "access_key_id", "secret_access_key",
		"deployment_id", "instance_id", "catalog",
	} {
		if !strings.Contains(stdout, field) {
			t.Errorf("configure usage never names %q", field)
		}
	}
	// Loading ignores unknown names, so a misspelling is dropped rather than
	// refused. That is a documented property because it is a trap.
	if !strings.Contains(stdout, "Unknown names are ignored") {
		t.Errorf("configure usage does not state that unknown names are ignored:\n%s", stdout)
	}

	var doc string
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			doc = trimmed
			break
		}
	}
	if doc == "" {
		t.Fatalf("configure usage shows no example document:\n%s", stdout)
	}
	var documented config.Config
	if err := json.Unmarshal([]byte(doc), &documented); err != nil {
		t.Fatalf("documented example %q is not JSON: %v", doc, err)
	}
	if err := config.Validate(documented); err != nil {
		t.Fatalf("documented example %q does not validate: %v", doc, err)
	}
	if documented.Repository == "" || documented.PasswordFile == "" {
		t.Fatalf("documented example %q fills in neither required field", doc)
	}
}

// Setup is the cheapest moment to fix a repository password other local
// accounts can read, and it was the one moment Babel said nothing: `storage
// status` reported the finding but `storage configure` accepted 0644 with
// exit 0 and a silent stderr. The property defended here is that configure
// now says it — naming the file, the observed mode, the expected mode, and
// the remedy, on stderr — while still installing the configuration, because
// refusing would strand an operator mid-setup with nothing written at all.
func TestStorageConfigureWarnsOnInsecurePasswordFileAndStillWrites(t *testing.T) {
	f := newFixture(t)
	password := filepath.Join(f.root, "password-loose")
	if err := os.WriteFile(password, []byte("synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Set rather than request: under umask 0077 the file above would land at
	// 0600 and this case would pass without ever exercising the warning.
	if err := os.Chmod(password, 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(config.Config{
		Repository:   filepath.Join(f.root, "configured-repo"),
		PasswordFile: password,
		HostID:       "configured-host",
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"storage", "configure", "--from-json", "-", "--json"}, bytes.NewReader(body), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("configure with a loose password file exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	warning := stderr.String()
	for _, want := range []string{password, "0644", "expected 0600 or stricter", "chmod 600"} {
		if !strings.Contains(warning, want) {
			t.Errorf("configure warning %q does not mention %q", warning, want)
		}
	}

	// Warned, not refused: the document is on disk and reads back.
	saved, found, err := config.Load()
	if err != nil || !found {
		t.Fatalf("configure did not install the document: found=%v err=%v", found, err)
	}
	if saved.PasswordFile != password || saved.HostID != "configured-host" {
		t.Fatalf("installed configuration = %+v", saved)
	}

	// The same rule, one implementation: status reaches the identical verdict
	// and the identical sentence about the same file.
	statusOut, statusErr := f.ok("storage", "status", "--json")
	if got := decode[storageStatusResult](t, statusOut); got.PasswordFileSecure {
		t.Fatalf("status called a 0644 password file secure: %+v", got)
	}
	if !strings.Contains(statusErr, "expected 0600 or stricter") {
		t.Fatalf("status warning = %q", statusErr)
	}
}

// ceremonyDocument renders what one custody path hands a machine: a storage
// configuration and, optionally, this deployment's payload key ring.
func ceremonyDocument(t *testing.T, cfg config.Config, ring *config.PayloadKeys) string {
	t.Helper()
	body, err := json.Marshal(config.ConfigureDocument{Config: cfg, PayloadKeys: ring})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// ceremonyConfig is a minimal local-mode configuration with a password file
// that exists at safe permissions, so the only diagnostics a test observes are
// the ones it is about.
func ceremonyConfig(t *testing.T, f *fixture) config.Config {
	t.Helper()
	password := filepath.Join(f.root, "ceremony-password")
	if err := os.WriteFile(password, []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Config{
		Repository:   filepath.Join(f.root, "ceremony-repo"),
		PasswordFile: password,
		HostID:       "ceremony-host",
	}
}

// payloadKey builds a ring entry from synthetic material. The bytes are
// recognizable so a test can assert they reached no stream, and they are not a
// real key: nothing in this repository's testdata is.
func payloadKey(t *testing.T, id, material string) config.PayloadKey {
	t.Helper()
	if len(material) != 32 {
		t.Fatalf("test key material is %d bytes, and a payload key is 32", len(material))
	}
	key, err := config.GeneratePayloadKey(id, []byte(material))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestStorageConfigureInstallsThePayloadKeyRing is the whole point of #112: one
// ceremony makes a fleet member whole.
//
// Before this, a second host was handed a configuration and no ring, so it read
// every plaintext catalog row and could open no record's content. The document
// that already carries this deployment's credentials now carries its ring, and
// the ring lands in the mode-0600 document `babel sync` has always read - never
// in storage.json, which holds no key material. Re-running the same ceremony
// must change nothing, because `atyrode provision babel` is re-run routinely.
func TestStorageConfigureInstallsThePayloadKeyRing(t *testing.T) {
	f := newFixture(t)
	first := payloadKey(t, "phase-b-1", "SYNTHETIC-CEREMONY-KEY-ONE-32-B!")
	second := payloadKey(t, "phase-b-2", "SYNTHETIC-CEREMONY-KEY-TWO-32-B!")
	document := ceremonyDocument(t, ceremonyConfig(t, f), &config.PayloadKeys{
		ActiveKeyID: "phase-b-2",
		Keys:        []config.PayloadKey{first, second},
	})

	stdout, stderr, code := f.runStdin(document, "storage", "configure", "--from-json", "-", "--json")
	if code != exitOK {
		t.Fatalf("configure with a ring exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	res := decode[storageConfigureResult](t, stdout)
	if res.PayloadKeys == nil {
		t.Fatal("the result does not report the ring, so a provisioning script cannot tell whether keys arrived")
	}
	if !res.PayloadKeys.Changed {
		t.Error("installing a ring on a machine that held none reported no change")
	}
	if got := res.PayloadKeys.Path; got != config.PayloadKeysPath() {
		t.Errorf("ring path = %q, want %q", got, config.PayloadKeysPath())
	}
	if got := res.PayloadKeys.ActiveKeyID; got != "phase-b-2" {
		t.Errorf("active key = %q, want phase-b-2", got)
	}
	if len(res.PayloadKeys.Added) != 2 {
		t.Errorf("added = %v, want both delivered keys", res.PayloadKeys.Added)
	}
	if len(res.PayloadKeys.AbsentFromDocument) != 0 {
		t.Errorf("absent_from_document = %v on a host that held nothing", res.PayloadKeys.AbsentFromDocument)
	}

	// The ring is where sync reads it, at the permissions that document has
	// always been written with, and storage.json carries no key material.
	info, err := os.Stat(config.PayloadKeysPath())
	if err != nil {
		t.Fatalf("the ceremony wrote no payload key document: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("payload key document mode = %04o, want 0600", mode)
	}
	keys, found, err := config.LoadPayloadKeys()
	if err != nil || !found || len(keys.Keys) != 2 || keys.ActiveKeyID != "phase-b-2" {
		t.Fatalf("installed ring = (%+v, found=%v, err=%v)", keys, found, err)
	}
	storage, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys.Keys {
		if strings.Contains(string(storage), key.Key) {
			t.Fatal("key material was written into storage.json, which is frozen and holds no keys")
		}
		assertNoMaterial(t, "configure", key.Key, stdout, stderr)
	}

	// A re-provision delivering the ring this host already holds must not
	// rewrite the one file in Babel that is nothing but key material.
	before, err := os.ReadFile(config.PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = f.runStdin(document, "storage", "configure", "--from-json", "-", "--json")
	if code != exitOK {
		t.Fatalf("re-provision exited %d\nstderr: %s", code, stderr)
	}
	again := decode[storageConfigureResult](t, stdout)
	if again.PayloadKeys == nil || again.PayloadKeys.Changed || len(again.PayloadKeys.Added) != 0 {
		t.Errorf("re-provision reported %+v, want no change", again.PayloadKeys)
	}
	after, err := os.ReadFile(config.PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a re-provision that delivered nothing new rewrote the payload key document")
	}
}

// TestStorageConfigureKeepsKeysTheDocumentOmits is the operator's backfill
// case, and the one thing this command warns about.
//
// The workstation generated a key before the ceremony carried rings, so the
// delivered document does not have it. Dropping it would orphan every object
// sealed under it - Babel deletes no remote object, so those objects would stay
// unreadable forever - and keeping it silently would hide that the key exists
// on exactly one disk. So it is kept, and the operator is told, by key id and
// with the file to copy from, never with the material.
func TestStorageConfigureKeepsKeysTheDocumentOmits(t *testing.T) {
	f := newFixture(t)
	local := payloadKey(t, "workstation-1", "SYNTHETIC-PRE-CEREMONY-KEY-32-B!")
	if err := config.AddPayloadKey(local, true); err != nil {
		t.Fatalf("seed the pre-ceremony key: %v", err)
	}
	delivered := payloadKey(t, "vault-1", "SYNTHETIC-CUSTODY-KEY-ONE-32-BY!")
	document := ceremonyDocument(t, ceremonyConfig(t, f), &config.PayloadKeys{
		ActiveKeyID: "vault-1",
		Keys:        []config.PayloadKey{delivered},
	})

	stdout, stderr, code := f.runStdin(document, "storage", "configure", "--from-json", "-", "--json")
	if code != exitOK {
		t.Fatalf("configure exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	res := decode[storageConfigureResult](t, stdout)
	if res.PayloadKeys == nil || !slices.Equal(res.PayloadKeys.AbsentFromDocument, []string{"workstation-1"}) {
		t.Fatalf("the result does not name the key custody lacks: %+v", res.PayloadKeys)
	}
	// The remedy is in the message, because the operator cannot infer it: the
	// document field to fill and the file to fill it from.
	for _, want := range []string{"workstation-1", "payload_keys", config.PayloadKeysPath()} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the warning does not mention %q:\n%s", want, stderr)
		}
	}

	keys, found, err := config.LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("LoadPayloadKeys = (found=%v, err=%v)", found, err)
	}
	if len(keys.Keys) != 2 {
		t.Fatalf("the merged ring holds %d keys, want the delivered one and the retained one: %+v",
			len(keys.Keys), keys.Keys)
	}
	if keys.ActiveKeyID != "vault-1" {
		t.Errorf("active key = %q: sealing follows custody, so the delivered ring names it", keys.ActiveKeyID)
	}
	for _, key := range keys.Keys {
		assertNoMaterial(t, "merge", key.Key, stdout, stderr)
	}
}

// TestStorageConfigureRefusesConflictingPayloadKeyMaterial pins the one
// refusal, and pins what a refusal costs.
//
// Two different keys under one id is a fork of the deployment's key space: the
// id is what selects the key that opens a record, so whichever side loses, its
// records stop opening. Nothing on this machine can tell which side is
// authoritative. So it refuses, and it refuses before replacing anything - the
// machine keeps the configuration and the ring it had.
func TestStorageConfigureRefusesConflictingPayloadKeyMaterial(t *testing.T) {
	f := newFixture(t)
	held := payloadKey(t, "phase-b-1", "SYNTHETIC-HELD-CEREMONY-KEY-32B!")
	if err := config.AddPayloadKey(held, true); err != nil {
		t.Fatal(err)
	}
	installed := ceremonyConfig(t, f)
	if _, _, code := f.runStdin(ceremonyDocument(t, installed, nil),
		"storage", "configure", "--from-json", "-"); code != exitOK {
		t.Fatal("configuring without a ring must succeed")
	}
	beforeConfig, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeRing, err := os.ReadFile(config.PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}

	// Same id, different bytes, and a repository the machine is not using, so
	// a document that displaced the installed one would be visible.
	conflicting := installed
	conflicting.Repository = filepath.Join(f.root, "displaced-repo")
	document := ceremonyDocument(t, conflicting, &config.PayloadKeys{
		ActiveKeyID: "phase-b-1",
		Keys:        []config.PayloadKey{payloadKey(t, "phase-b-1", "SYNTHETIC-OTHER-CEREMONY-KEY-32!")},
	})
	stdout, stderr, code := f.runStdin(document, "storage", "configure", "--from-json", "-")
	if code != exitFailure {
		t.Fatalf("a conflicting key exited %d, want %d\nstdout: %s\nstderr: %s",
			code, exitFailure, stdout, stderr)
	}
	if !strings.Contains(stderr, "phase-b-1") {
		t.Errorf("the refusal does not name the key id an operator has to reconcile:\n%s", stderr)
	}

	afterConfig, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(afterConfig) != string(beforeConfig) {
		t.Error("a refused ceremony replaced the storage configuration this machine was working with")
	}
	afterRing, err := os.ReadFile(config.PayloadKeysPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRing) != string(beforeRing) {
		t.Error("a refused ceremony rewrote the payload key document")
	}
	assertNoMaterial(t, "conflict", held.Key, stdout, stderr)
}
