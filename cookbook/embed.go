// Package cookbookassets embeds Babel's versioned analysis cookbook (SPEC.md
// §5) into the babel binary, so a built binary carries the exact recipe text
// and version manifest it was compiled from rather than depending on files
// beside it at runtime.
//
// The assets live here, at the repository root, because they are a reviewable
// public product asset rather than internal Go code: a recipe diff is the unit
// a human reviews when the cookbook changes. The embed lives in its own
// package because go:embed paths cannot cross package directories, which is
// the same reason web/embed.go exists.
//
// internal/cookbook owns parsing, validation, and the version-drift check.
// Nothing here interprets the assets.
package cookbookassets

import (
	"embed"
	"io/fs"
)

//go:embed recipes versions.json
var assets embed.FS

// Assets is the cookbook asset tree: a `recipes` directory of Markdown
// recipes plus `versions.json`, the committed record of each recipe's declared
// version and content digest.
func Assets() fs.FS {
	return assets
}
