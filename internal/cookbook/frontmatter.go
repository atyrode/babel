package cookbook

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/atyrode/babel/internal/worker"
)

// fence is the front-matter delimiter line. It opens line 1 of every recipe
// and closes the block.
const fence = "---"

// The documented front-matter keys (SPEC.md §5.1). Every one is required:
// making `default` explicit on every recipe means no recipe is enabled by
// omission, and a reader never has to know the loader's defaults to know what
// a recipe does.
const (
	keyID           = "id"
	keyVersion      = "version"
	keyKind         = "kind"
	keyScope        = "scope"
	keyStages       = "stages"
	keyCapabilities = "capabilities"
	keyDefault      = "default"
)

// frontMatterKeys is the accepted key set in document order. Recipes are
// authored in this order; the loader does not require it, because key order
// carries no meaning, while an unknown key does.
var frontMatterKeys = []string{
	keyID, keyVersion, keyKind, keyScope, keyStages, keyCapabilities, keyDefault,
}

// ParseError names the exact line of a recipe that violates the grammar.
//
// The line number matters more than it looks: the front-matter grammar is
// deliberately narrow, so most authoring mistakes are grammar errors, and an
// error that only said "invalid recipe" would send the author reading a
// several-hundred-line document. Line 1 is the opening fence, so these numbers
// are the ones an editor shows.
type ParseError struct {
	Path string
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
}

func parseErrorf(path string, line int, format string, args ...any) *ParseError {
	return &ParseError{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// frontMatter is the parsed machine-readable header of a recipe.
type frontMatter struct {
	id           string
	version      int
	kind         Kind
	scope        []Scope
	stages       []Stage
	capabilities []worker.Capability
	enabled      bool
}

// parseFrontMatter parses the front-matter block of a recipe and returns it
// with the body that follows and the 1-based line number the body starts on.
//
// The grammar is hand-rolled rather than delegated to a YAML library, and it
// accepts strictly less than YAML: scalars, integers, booleans, and
// flow-style lists of bare tokens, one `key: value` per line, no indentation,
// no comments, no blank lines, no quoting, no tabs, and no multi-line values.
// A restricted grammar we control has no aliases, no merge keys, no type
// coercion surprises, and no dependency; anything outside it is a load error
// instead of a guess.
func parseFrontMatter(path string, data []byte) (frontMatter, string, int, error) {
	var fm frontMatter

	lines := strings.Split(string(data), "\n")
	if lines[0] != fence {
		return fm, "", 0, parseErrorf(path, 1, "recipe must begin with a %q front-matter line", fence)
	}

	// The terminator is located before any line is parsed, so an unterminated
	// block is reported as exactly that rather than as a complaint about
	// whatever the first Markdown line of the body happens to be.
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == fence {
			end = i
			break
		}
	}
	if end < 0 {
		last := len(lines)
		if lines[last-1] == "" {
			last--
		}
		return fm, "", 0, parseErrorf(path, last,
			"front matter is never closed by a %q line", fence)
	}

	seen := make(map[string]int, len(frontMatterKeys))
	for i := 1; i < end; i++ {
		if err := parseFrontMatterLine(path, i+1, lines[i], &fm, seen); err != nil {
			return fm, "", 0, err
		}
	}

	// A missing key has no line of its own, so it is reported against the
	// closing fence: that is the line the author must insert one above.
	for _, key := range frontMatterKeys {
		if _, ok := seen[key]; !ok {
			return fm, "", 0, parseErrorf(path, end+1, "front matter is missing required key %q", key)
		}
	}
	if fm.enabled && fm.kind != KindLens {
		return fm, "", 0, parseErrorf(path, seen[keyDefault],
			"only a %s may be default-enabled, and this recipe is a %s", KindLens, fm.kind)
	}
	if len(fm.scope) == 0 {
		return fm, "", 0, parseErrorf(path, seen[keyScope], "recipe must declare at least one scope")
	}
	if len(fm.stages) == 0 {
		return fm, "", 0, parseErrorf(path, seen[keyStages], "recipe must declare at least one stage")
	}

	return fm, strings.Join(lines[end+1:], "\n"), end + 2, nil
}

// parseFrontMatterLine parses one `key: value` line into fm, recording the key
// and its line in seen.
func parseFrontMatterLine(path string, num int, line string, fm *frontMatter, seen map[string]int) error {
	switch {
	case line == "":
		return parseErrorf(path, num, "front matter allows no blank line")
	case strings.Contains(line, "\t"):
		return parseErrorf(path, num, "front matter allows no tab character")
	case strings.Contains(line, "\r"):
		return parseErrorf(path, num, "front matter allows no carriage return; recipes use LF line endings")
	case strings.HasPrefix(line, " "):
		return parseErrorf(path, num, "front matter allows no indentation")
	case strings.HasSuffix(line, " "):
		return parseErrorf(path, num, "front matter allows no trailing space")
	case strings.HasPrefix(line, "#"):
		return parseErrorf(path, num, "front matter allows no comment")
	}

	colon := strings.Index(line, ":")
	if colon < 0 {
		return parseErrorf(path, num, "line is not a %q pair", "key: value")
	}
	key, rest := line[:colon], line[colon+1:]
	if !strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "  ") {
		return parseErrorf(path, num, "exactly one space must follow the colon of key %q", key)
	}
	value := rest[1:]

	if !knownKey(key) {
		return parseErrorf(path, num, "unknown front-matter key %q; accepted keys are %s",
			key, strings.Join(frontMatterKeys, ", "))
	}
	if first, ok := seen[key]; ok {
		return parseErrorf(path, num, "duplicate front-matter key %q, first set on line %d", key, first)
	}
	seen[key] = num

	switch key {
	case keyID:
		if !validID(value) {
			return parseErrorf(path, num,
				"id %q must be lowercase letters, digits, and single interior hyphens", value)
		}
		fm.id = value
	case keyVersion:
		version, err := parseInt(value)
		if err != nil {
			return parseErrorf(path, num, "%s: %v", keyVersion, err)
		}
		fm.version = version
	case keyKind:
		kind := Kind(value)
		if !kind.Known() {
			return parseErrorf(path, num, "kind %q is not one of %s", value, joinKinds())
		}
		fm.kind = kind
	case keyScope:
		items, err := parseList(value)
		if err != nil {
			return parseErrorf(path, num, "%s: %v", keyScope, err)
		}
		for _, item := range items {
			s := Scope(item)
			if !s.Known() {
				return parseErrorf(path, num, "scope %q is not one of %s", item, joinScopes())
			}
			fm.scope = append(fm.scope, s)
		}
	case keyStages:
		items, err := parseList(value)
		if err != nil {
			return parseErrorf(path, num, "%s: %v", keyStages, err)
		}
		for _, item := range items {
			s := Stage(item)
			if !s.Known() {
				return parseErrorf(path, num, "stage %q is not one of %s", item, joinStages())
			}
			fm.stages = append(fm.stages, s)
		}
	case keyCapabilities:
		items, err := parseList(value)
		if err != nil {
			return parseErrorf(path, num, "%s: %v", keyCapabilities, err)
		}
		for _, item := range items {
			c := worker.Capability(item)
			if !c.Known() {
				return parseErrorf(path, num, "capability %q is not one Babel defines: %s",
					item, joinCapabilities())
			}
			fm.capabilities = append(fm.capabilities, c)
		}
	case keyDefault:
		switch value {
		case "true":
			fm.enabled = true
		case "false":
			fm.enabled = false
		default:
			return parseErrorf(path, num, "%s must be true or false, not %q", keyDefault, value)
		}
	}
	return nil
}

func knownKey(key string) bool {
	for _, k := range frontMatterKeys {
		if k == key {
			return true
		}
	}
	return false
}

// parseInt accepts a positive decimal integer with no sign, no leading zero,
// and no separators. Recipe versions start at 1 and only ever increase, so
// there is no form to be lenient about.
func parseInt(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("value is empty, want a positive integer")
	}
	if value[0] == '0' {
		return 0, fmt.Errorf("%q has a leading zero, want a positive integer", value)
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q is not a positive integer", value)
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a positive integer: %w", value, err)
	}
	return n, nil
}

// parseList accepts a flow-style list of bare tokens: `[a, b]`, `[a,b]`, or
// the empty `[]`. Block sequences, quoting, nesting, and trailing commas are
// rejected, and a repeated item is rejected too, because a repeated scope or
// capability is a mistake with no meaning to preserve.
func parseList(value string) ([]string, error) {
	if !strings.HasPrefix(value, "[") {
		return nil, fmt.Errorf("%q is not a flow-style list like [a, b]", value)
	}
	if !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("list %q is not closed with %q", value, "]")
	}
	inner := value[1 : len(value)-1]
	if strings.TrimSpace(inner) == "" {
		if inner != "" {
			return nil, fmt.Errorf("empty list must be written %q", "[]")
		}
		return nil, nil
	}

	parts := strings.Split(inner, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			return nil, fmt.Errorf("list %q has an empty item", value)
		}
		if strings.ContainsAny(item, "[]\"' ") {
			return nil, fmt.Errorf("list item %q is not a bare token", item)
		}
		for _, have := range items {
			if have == item {
				return nil, fmt.Errorf("list %q repeats item %q", value, item)
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// validID accepts the identifier form every recipe id and list token uses:
// lowercase letters and digits joined by single hyphens. It is also the file
// name, so it stays free of anything a path or a URL would have to escape.
func validID(value string) bool {
	if value == "" {
		return false
	}
	prevHyphen := false
	for i, c := range value {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevHyphen = false
		case c == '-':
			if i == 0 || prevHyphen {
				return false
			}
			prevHyphen = true
		default:
			return false
		}
	}
	return !prevHyphen
}
