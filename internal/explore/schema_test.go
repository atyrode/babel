package explore_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/worker"
)

// schemaKeywords is the vocabulary the generated schema may use. The worker
// validates the contract with a validator of its own and refuses a keyword it
// does not implement, so a keyword outside this list is a run refused at the
// preamble rather than a richer schema.
var schemaKeywords = []string{"$schema", "$defs", "$ref", "type", "properties", "required", "additionalProperties", "items", "enum"}

// TestOutputContractFollowsTheAuthorityTable is the schema half of the stage
// contract: each stage is offered exactly the fields persistence would accept
// from it, the vocabulary stays inside what the worker implements, and every
// reference resolves inside the document.
func TestOutputContractFollowsTheAuthorityTable(t *testing.T) {
	cases := []struct {
		stage     explore.Stage
		root      []string
		candidate []string
	}{
		// "questions" is offered to all three: §5.4 divides what a stage
		// may assert, and a question asserts nothing.
		{explore.StageExplore, []string{"candidates", "consolidations", "deferred", "rejected", "questions"},
			[]string{"ref", "hypothesis", "observations", "remedy", "dispositions"}},
		{explore.StageChallenge, []string{"candidates", "objections", "questions"},
			[]string{"ref", "hypothesis", "dispositions"}},
		{explore.StageSynthesize, []string{"candidates", "consolidations", "questions"},
			[]string{"ref", "hypothesis", "remedy", "dispositions"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			contract := explore.OutputContract(tc.stage)
			if contract.Schema != worker.ResultSchema {
				t.Errorf("schema id = %q, want %q", contract.Schema, worker.ResultSchema)
			}
			if contract.Instructions == "" {
				t.Error("the stage carries no instructions")
			}
			doc := decodeSchema(t, contract.JSONSchema)
			slices.Sort(tc.root)
			slices.Sort(tc.candidate)
			if got := propertyNames(doc); !slices.Equal(got, tc.root) {
				t.Errorf("root properties = %v, want %v", got, tc.root)
			}
			defs := doc["$defs"].(map[string]any)
			if got := propertyNames(defs["Candidate"].(map[string]any)); !slices.Equal(got, tc.candidate) {
				t.Errorf("Candidate properties = %v, want %v", got, tc.candidate)
			}
			var keywords []string
			walkKeywords(doc, false, func(k string) {
				if !slices.Contains(keywords, k) {
					keywords = append(keywords, k)
				}
			})
			for _, k := range keywords {
				if !slices.Contains(schemaKeywords, k) {
					t.Errorf("schema uses keyword %q, outside the vocabulary the worker implements", k)
				}
			}
			walkRefs(doc, func(ref string) {
				name, ok := strings.CutPrefix(ref, "#/$defs/")
				if !ok {
					t.Errorf("reference %q is not local to the document", ref)
				} else if _, defined := defs[name]; !defined {
					t.Errorf("reference %q names no definition", ref)
				}
			})
			for name := range defs {
				if !strings.Contains(string(contract.JSONSchema), `"#/$defs/`+name+`"`) {
					t.Errorf("definition %q is unreferenced in this stage's schema", name)
				}
			}
		})
	}
}

// TestGeneratedSchemaDescribesTheStoredShape proves the schema is the real
// payload shape rather than a description beside it: the harness's own
// well-formed result conforms, an evidence citation is the locator object the
// frontier marshals, and a result carrying a field the type does not declare
// is refused by the same schema.
func TestGeneratedSchemaDescribesTheStoredShape(t *testing.T) {
	h := newHarness(t)
	schema := explore.OutputContract(explore.StageExplore).JSONSchema
	encoded, err := json.Marshal(h.discovery())
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if err := conforms(schema, encoded); err != nil {
		t.Fatalf("the harness's result does not conform to the schema it would be handed: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	doc["candidates"].([]any)[0].(map[string]any)["confidence_score"] = 0.9
	altered, _ := json.Marshal(doc)
	if err := conforms(schema, altered); err == nil {
		t.Error("a field the result type does not declare passed the schema")
	}

	// The schema of a challenger refuses what persistence would drop.
	challenge := explore.OutputContract(explore.StageChallenge).JSONSchema
	if err := conforms(challenge, encoded); err == nil {
		t.Error("a developed, consolidated result passed the challenger's schema")
	}
}

func decodeSchema(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return doc
}

func propertyNames(s map[string]any) []string {
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// walkKeywords visits every schema keyword in the document. The values of
// properties and $defs are named by the payload, not by the vocabulary, so
// their keys are skipped and their values descended.
func walkKeywords(v any, named bool, visit func(string)) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if !named {
				visit(k)
			}
			walkKeywords(x, k == "properties" || k == "$defs", visit)
		}
	case []any:
		for _, x := range v {
			walkKeywords(x, false, visit)
		}
	}
}

func walkRefs(v any, visit func(string)) {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			visit(ref)
		}
		for _, x := range v {
			walkRefs(x, visit)
		}
	case []any:
		for _, x := range v {
			walkRefs(x, visit)
		}
	}
}

// conforms checks a document against the generated schema's vocabulary. It is
// deliberately only that vocabulary: the worker's validator is the authority,
// and this exists so a Babel-side test can say a payload Babel stores is one
// the schema admits without a validator dependency.
func conforms(schema, document json.RawMessage) error {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(document, &doc); err != nil {
		return err
	}
	defs, _ := root["$defs"].(map[string]any)
	var check func(path string, s map[string]any, v any) error
	check = func(path string, s map[string]any, v any) error {
		if ref, ok := s["$ref"].(string); ok {
			target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			if !ok {
				return fmt.Errorf("%s: unresolved %s", path, ref)
			}
			return check(path, target, v)
		}
		if enum, ok := s["enum"].([]any); ok && !slices.Contains(enum, v) {
			return fmt.Errorf("%s: %v is not one of %v", path, v, enum)
		}
		switch s["type"] {
		case "object":
			obj, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: %v is not an object", path, v)
			}
			props, _ := s["properties"].(map[string]any)
			for _, r := range s["required"].([]any) {
				if _, ok := obj[r.(string)]; !ok {
					return fmt.Errorf("%s: missing %s", path, r)
				}
			}
			for k, x := range obj {
				sub, ok := props[k].(map[string]any)
				if !ok {
					return fmt.Errorf("%s: property %q is not declared", path, k)
				}
				if err := check(path+"."+k, sub, x); err != nil {
					return err
				}
			}
		case "array":
			items, ok := v.([]any)
			if !ok {
				return fmt.Errorf("%s: %v is not an array", path, v)
			}
			for i, x := range items {
				if err := check(fmt.Sprintf("%s[%d]", path, i), s["items"].(map[string]any), x); err != nil {
					return err
				}
			}
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s: %v is not a string", path, v)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s: %v is not a boolean", path, v)
			}
		case "integer", "number":
			if _, ok := v.(float64); !ok {
				return fmt.Errorf("%s: %v is not a number", path, v)
			}
		default:
			return fmt.Errorf("%s: schema type %v is not one this test knows", path, s["type"])
		}
		return nil
	}
	return check("$", root, doc)
}
