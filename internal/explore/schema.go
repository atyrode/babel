package explore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

// OutputContract is what one stage's job tells the worker to produce: the
// result schema identifier, a JSON Schema generated from Result and the
// frontier payload types it embeds, and the stage's instructions.
//
// The schema is generated rather than written, and that is the point of it.
// Result reuses internal/frontier's payload types so that what a worker
// proposes and what Babel stores are one declaration; a hand-maintained
// schema would be the second declaration that reasoning refuses, one that a
// new payload field silently misses. Generating it from the types at start-up
// means a field added to a payload reaches the model on the next run, with no
// edit here and none in the worker.
//
// The authority table shapes it per stage, so a challenger is not offered a
// consolidations field it would be refused for filling: the schema says what
// the stage may emit, and persistence enforces the same table on what it did.
//
// The instructions are static text. Run-specific inputs belong in the job's
// prompt, which is sent only after Code's runtime report passes admission.
func OutputContract(stage Stage) worker.OutputContract {
	contract, ok := stageContracts[stage]
	if !ok {
		panic(fmt.Sprintf("explore: no output contract for stage %q", stage))
	}
	return contract
}

var stageContracts = func() map[Stage]worker.OutputContract {
	contracts := make(map[Stage]worker.OutputContract, len(authorities))
	for stage, auth := range authorities {
		schema, err := resultSchema(auth)
		if err != nil {
			// A payload type this generator cannot describe is a build
			// defect, not a runtime condition: every run would hand the
			// worker a schema that lies about the shape Babel stores.
			panic(fmt.Sprintf("explore: %s result schema: %v", stage, err))
		}
		contracts[stage] = worker.OutputContract{
			Schema:       worker.ResultSchema,
			JSONSchema:   schema,
			Instructions: stageInstructions(stage, auth),
		}
	}
	return contracts
}()

// resultSchema generates the JSON Schema (draft 2020-12) of Result as one
// stage may fill it. The native OMP host-tool interface validates this schema;
// Babel does not maintain a second JSON Schema validator.
func resultSchema(auth authority) (json.RawMessage, error) {
	g := &schemaGenerator{defs: map[string]*object{}}
	if _, err := g.describe(reflect.TypeFor[Result]()); err != nil {
		return nil, err
	}
	// The root is inlined rather than referenced so the document reads as
	// the object it describes; every nested named type is a definition.
	name := reflect.TypeFor[Result]().Name()
	root := g.defs[name]
	delete(g.defs, name)

	// The authority table prunes what the stage may not emit, in the same
	// terms persistence refuses it. Candidates stay for every stage: a
	// challenger's alternative and a synthesizer's addition are both
	// hypotheses §5.2 persists.
	if !auth.objections {
		root.remove("objections")
	}
	if !auth.consolidate {
		root.remove("consolidations")
	}
	if !auth.schedule {
		root.remove("deferred")
		root.remove("rejected")
	}
	if candidate, ok := g.defs[reflect.TypeFor[Candidate]().Name()]; ok {
		if !auth.observations {
			candidate.remove("observations")
		}
		if !auth.remedies {
			candidate.remove("remedy")
		}
	}

	doc := &object{}
	doc.set("$schema", "https://json-schema.org/draft/2020-12/schema")
	for _, key := range root.keys {
		doc.set(key, root.get(key))
	}
	defs := &object{}
	for _, name := range g.reachable(root) {
		defs.set(name, g.defs[name])
	}
	if len(defs.keys) > 0 {
		doc.set("$defs", defs)
	}
	return json.Marshal(doc)
}

// object is a JSON object that marshals its keys in insertion order, so a
// schema's properties read in the order the Go type declares them rather
// than alphabetically. A model reads the schema as prose; "ref" before
// "hypothesis" before "observations" is the development path in order.
type object struct {
	keys []string
	vals map[string]any
}

func (o *object) set(key string, val any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

func (o *object) get(key string) any { return o.vals[key] }

// remove drops one property from an object schema, from its required list
// too, so the pruned schema stays consistent with itself.
func (o *object) remove(property string) {
	props, _ := o.get("properties").(*object)
	if props == nil {
		return
	}
	if _, ok := props.vals[property]; !ok {
		return
	}
	delete(props.vals, property)
	props.keys = slices.DeleteFunc(props.keys, func(k string) bool { return k == property })
	if required, ok := o.get("required").([]string); ok {
		o.set("required", slices.DeleteFunc(required, func(k string) bool { return k == property }))
	}
}

func (o *object) MarshalJSON() ([]byte, error) {
	var buf strings.Builder
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(o.vals[key])
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(val)
	}
	buf.WriteByte('}')
	return []byte(buf.String()), nil
}

// enumerated is what a closed string vocabulary implements so the schema can
// list it. Each such type's validator reads the same list, which is what keeps
// the schema's enum and Babel's refusal in agreement.
type enumerated interface{ Values() []string }

// wireShaped is what a type with a custom JSON encoding implements so the
// generator describes the bytes it writes rather than the fields it holds.
// frontier.Evidence is the case: its fields are unexported so that no citation
// can exist without a locator, and reflection over them would describe an
// empty object.
type wireShaped interface{ WireShape() any }

var (
	enumeratedType  = reflect.TypeFor[enumerated]()
	wireShapedType  = reflect.TypeFor[wireShaped]()
	marshalerType   = reflect.TypeFor[json.Marshaler]()
	unmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

type schemaGenerator struct {
	// defs holds every named struct type described so far, keyed by its
	// Go type name; sources records which type owns each name so two
	// packages' same-named types are an error rather than one definition.
	defs    map[string]*object
	sources map[string]reflect.Type
}

// describe returns the schema for t: a $ref for a named struct, which it
// defines on first sight, and an inline schema for everything else.
func (g *schemaGenerator) describe(t reflect.Type) (any, error) {
	if t.Kind() == reflect.Pointer {
		// A pointer is an optional field's way of being absent, and absence
		// is expressed by the field not being required. Null is never a
		// value a worker writes.
		return g.describe(t.Elem())
	}
	if t == reflect.TypeFor[time.Time]() {
		// time.Time marshals as an RFC 3339 string, and a tool argument
		// that takes a time takes it in that form.
		return typed("string"), nil
	}
	if t.Implements(enumeratedType) {
		values := reflect.Zero(t).Interface().(enumerated).Values()
		s := &object{}
		s.set("type", "string")
		s.set("enum", values)
		return s, nil
	}
	if t.Implements(wireShapedType) {
		shape := reflect.TypeOf(reflect.Zero(t).Interface().(wireShaped).WireShape())
		if shape.Kind() != reflect.Struct {
			return nil, fmt.Errorf("%s: wire shape %s is not a struct", t, shape)
		}
		return g.define(t.Name(), t, shape)
	}
	if t.Implements(marshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
		// A custom encoding the generator cannot see would be described
		// from fields the wire never carries.
		return nil, fmt.Errorf("%s encodes itself and declares no wire shape", t)
	}
	switch t.Kind() {
	case reflect.String:
		return typed("string"), nil
	case reflect.Bool:
		return typed("boolean"), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return typed("integer"), nil
	case reflect.Float32, reflect.Float64:
		return typed("number"), nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("%s: a byte slice encodes as base64, which no result field carries", t)
		}
		items, err := g.describe(t.Elem())
		if err != nil {
			return nil, err
		}
		s := typed("array")
		s.set("items", items)
		return s, nil
	case reflect.Struct:
		if t.Name() == "" {
			return nil, fmt.Errorf("anonymous struct %s has no name to define", t)
		}
		return g.define(t.Name(), t, t)
	}
	return nil, fmt.Errorf("%s: kind %s has no schema", t, t.Kind())
}

func typed(kind string) *object {
	s := &object{}
	s.set("type", kind)
	return s
}

// define records the object schema of shape under name and returns a
// reference to it.
func (g *schemaGenerator) define(name string, owner, shape reflect.Type) (any, error) {
	if g.sources == nil {
		g.sources = map[string]reflect.Type{}
	}
	ref := &object{}
	ref.set("$ref", "#/$defs/"+name)
	if prior, seen := g.sources[name]; seen {
		if prior != owner {
			return nil, fmt.Errorf("%s and %s would share the definition %q", prior, owner, name)
		}
		return ref, nil
	}
	g.sources[name] = owner
	// Reserve the name before descending so a self-referential type
	// terminates; the definition is filled in below.
	g.defs[name] = &object{}

	props := &object{}
	var required []string
	for i := range shape.NumField() {
		field := shape.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous {
			return nil, fmt.Errorf("%s.%s: embedded fields are not described", shape, field.Name)
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key, opts, _ := strings.Cut(tag, ",")
		if key == "" {
			key = field.Name
		}
		desc, err := g.describe(field.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", shape, field.Name, err)
		}
		props.set(key, desc)
		if !slices.Contains(strings.Split(opts, ","), "omitempty") {
			required = append(required, key)
		}
	}
	s := g.defs[name]
	s.set("type", "object")
	s.set("properties", props)
	if required == nil {
		required = []string{}
	}
	s.set("required", required)
	s.set("additionalProperties", false)
	return ref, nil
}

// reachable lists the definitions the root still references after pruning,
// in first-reference order, so a stage's schema carries no definition it
// cannot use.
func (g *schemaGenerator) reachable(root *object) []string {
	var (
		order []string
		seen  = map[string]bool{}
		walk  func(v any)
	)
	walk = func(v any) {
		switch v := v.(type) {
		case *object:
			if ref, ok := v.get("$ref").(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if !seen[name] {
					seen[name] = true
					order = append(order, name)
					walk(g.defs[name])
				}
				return
			}
			for _, key := range v.keys {
				walk(v.vals[key])
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(root)
	return order
}
