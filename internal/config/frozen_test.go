package config

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The storage document is frozen (SPEC.md §14, first gate, 2026-08-29). Data
// written under it must stay readable forever, so this test is the enforcement
// rather than the prose: it pins the exact set of JSON names the document can
// carry, at every level.
//
// A frozen contract does not mean unchangeable. It means a change is a
// deliberate act with a compatibility story, and the way to have that
// conversation is for this test to fail. Adding a field is a schema question:
// old readers ignore unknown names, so a *new optional* field is compatible and
// belongs here with its name recorded. Renaming or removing one is not
// compatible, and the failure is the point.
//
// Loading deliberately ignores unknown names so a newer writer's document stays
// readable, which is exactly why no other test can catch an accidental addition.
func TestFrozenDocumentFieldNames(t *testing.T) {
	// Schema 2, as deployed against real Cellar and real managed PostgreSQL on
	// 2026-08-29. Every name here is load-bearing for some deployment.
	frozen := map[string][]string{
		"Config": {
			"catalog",
			"config_schema",
			"deployment_id",
			"host_id",
			"instance_id",
			"mode",
			"password_file",
			"repository",
			"repository_store",
			"restic_binary",
		},
		"RepositoryStore": {
			"access_key_id",
			"secret_access_key",
		},
		"Catalog": {
			"database",
			"host",
			"migration_password",
			"migration_user",
			"password",
			"port",
			"tls_mode",
			"tls_root_ca_file",
			"user",
		},
	}

	for name, want := range map[string][]string{
		"Config":          frozen["Config"],
		"RepositoryStore": frozen["RepositoryStore"],
		"Catalog":         frozen["Catalog"],
	} {
		var got []string
		switch name {
		case "Config":
			got = jsonNames(reflect.TypeOf(Config{}))
		case "RepositoryStore":
			got = jsonNames(reflect.TypeOf(RepositoryStore{}))
		case "Catalog":
			got = jsonNames(reflect.TypeOf(Catalog{}))
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s JSON names changed.\n  frozen: %v\n  now:    %v\n"+
				"A new optional name is compatible: record it above.\n"+
				"A rename or removal is not: old documents would stop loading.",
				name, want, got)
		}
	}
}

// The schema number itself is frozen: documents record it, and Load refuses a
// number newer than this build. Bumping it is how an incompatible change
// announces itself, so it must not move by accident.
func TestFrozenSchemaNumber(t *testing.T) {
	if currentSchema != 2 {
		t.Fatalf("currentSchema = %d, frozen at 2 (SPEC.md §14). "+
			"Raising this is an incompatible-change announcement, not a bump.",
			currentSchema)
	}
}

// A document written before the freeze must still load. The golden fixtures
// cover byte-stable round trips; this covers the older shape that predates both
// mode and the object-store credential, because a schema-1 local document is
// what an early developer install has on disk.
func TestFrozenSchemaOneStillLoads(t *testing.T) {
	const schemaOne = `{
	  "config_schema": 1,
	  "repository": "/srv/babel/repo",
	  "password_file": "/etc/babel/password"
	}`
	var cfg Config
	if err := json.NewDecoder(strings.NewReader(schemaOne)).Decode(&cfg); err != nil {
		t.Fatalf("a schema-1 document no longer decodes: %v", err)
	}
	if cfg.Mode != "" {
		t.Errorf("schema-1 document gained a mode from decoding: %q", cfg.Mode)
	}
	if cfg.RepositoryStore != nil {
		t.Error("schema-1 document gained an object-store credential from decoding")
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("a valid schema-1 local document stopped validating: %v", err)
	}
}

// jsonNames reports the JSON names a struct serializes, ignoring names the
// encoder would skip.
func jsonNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}
