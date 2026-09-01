package cookbook

import (
	"fmt"
	"strings"

	"github.com/atyrode/babel/internal/digest"
)

// preambleFile is the cookbook's standing statement, beside the recipes it
// governs.
const preambleFile = "preamble.md"

// preambleID is the identity the statement is recorded and reported under. It
// shares the recipes' namespace — one drift report names either — which is why
// no recipe may take it.
const preambleID = "preamble"

// Preamble is what the cookbook says about itself: SPEC.md §1's charter
// restated where recipes are written and reviewed, so their evolution is
// measured against the principle rather than against the taste of whoever last
// edited the directory (#120).
//
// It is versioned and digested exactly like a recipe, and for the same §5.1
// reason. The statement is the standard recipe changes are judged by, so a
// silently rewritten standard is worse than a silently rewritten recipe: every
// review that cited it would be citing text that no longer exists. Version
// declares intent, the digest records content, and the two disagreeing is
// drift.
//
// Its grammar is deliberately smaller than a recipe's. A recipe carries the
// front matter a run needs — kind, scope, stages, capabilities — and none of
// that applies to a document no run is briefed with: the preamble reaches
// people, through review and through `babel cookbook list`, so all it declares
// is the version its content is recorded under.
type Preamble struct {
	// Version is the declared version of this statement, which moves when
	// its content does.
	Version int
	// Title is the body's level-1 heading.
	Title string
	// Path is the statement's location inside the asset tree it was loaded
	// from, so an error can be traced to a file.
	Path string
	// Body is the Markdown following the front matter, verbatim.
	Body string
	// Digest covers the body alone. Version is excluded for the same reason
	// it is excluded from a recipe's digest: including it would make every
	// increment look like a content change and destroy the drift check's
	// only signal.
	Digest digest.Digest
}

// ParsePreamble parses and validates the cookbook's statement. It is exported
// beside ParseRecipe so an authoring or review tool can check a candidate
// before it is placed in the asset tree.
func ParsePreamble(name string, data []byte) (*Preamble, error) {
	version, body, bodyLine, err := parsePreambleHeader(name, data)
	if err != nil {
		return nil, err
	}
	title, err := parsePreambleBody(name, body, bodyLine)
	if err != nil {
		return nil, err
	}
	// §5.1's prohibition is about guidance, not about the file it sits in: a
	// preamble naming a provider would put the choice recipes may not make
	// into the document recipes are written against.
	if mentions := VendorMentions(body); len(mentions) > 0 {
		return nil, fmt.Errorf("cookbook: %s names %s; §5.1 forbids cookbook guidance from selecting a provider or model",
			name, strings.Join(mentions, ", "))
	}

	p := &Preamble{
		Version: version,
		Title:   title,
		Path:    name,
		Body:    body,
	}
	p.Digest = digest.Bytes([]byte(body))
	return p, nil
}

// parsePreambleHeader parses the statement's front matter — one `version` line
// between two fences — and returns it with the body and the 1-based line the
// body starts on.
func parsePreambleHeader(path string, data []byte) (int, string, int, error) {
	lines := strings.Split(string(data), "\n")
	if lines[0] != fence {
		return 0, "", 0, parseErrorf(path, 1, "preamble must begin with a %q front-matter line", fence)
	}

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
		return 0, "", 0, parseErrorf(path, last, "front matter is never closed by a %q line", fence)
	}
	if end != 2 {
		return 0, "", 0, parseErrorf(path, 2,
			"front matter must be the single line %q; the preamble declares nothing a run resolves", keyVersion+": N")
	}

	key, rest, ok := strings.Cut(lines[1], ":")
	if !ok || key != keyVersion {
		return 0, "", 0, parseErrorf(path, 2, "front matter must declare %q", keyVersion)
	}
	if !strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "  ") {
		return 0, "", 0, parseErrorf(path, 2, "exactly one space must follow the colon of key %q", keyVersion)
	}
	version, err := parseInt(rest[1:])
	if err != nil {
		return 0, "", 0, parseErrorf(path, 2, "%s: %v", keyVersion, err)
	}
	return version, strings.Join(lines[end+1:], "\n"), end + 2, nil
}

// parsePreambleBody checks the little structure the statement has and returns
// its title. There is no required section list: a recipe's sections exist so a
// consumer can rely on the shape, and this document's only consumer is a
// person reading it.
func parsePreambleBody(name, body string, bodyLine int) (string, error) {
	title := ""
	stated := false
	for i, line := range strings.Split(body, "\n") {
		num := bodyLine + i
		switch {
		case strings.HasPrefix(line, "# "):
			if title != "" {
				return "", parseErrorf(name, num, "body has a second level-1 heading; the preamble has one title")
			}
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case strings.TrimSpace(line) == "":
		case title == "":
			return "", parseErrorf(name, num, "body must open with a level-1 title, found %q", strings.TrimSpace(line))
		default:
			stated = true
		}
	}
	if title == "" {
		return "", parseErrorf(name, bodyLine, "body has no level-1 title")
	}
	if !stated {
		return "", parseErrorf(name, bodyLine, "body is a title with no statement under it")
	}
	return title, nil
}
