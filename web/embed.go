// Package webdist embeds the built web application (web/dist) into the
// babel binary. The frontend sources live beside this file; `bun run build`
// regenerates dist, and the committed dist is what ships. The embed lives in
// its own package because go:embed paths cannot cross package directories.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built single-page application rooted at its index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The embed directive guarantees dist exists at compile time, so
		// this is unreachable rather than a runtime condition.
		panic(err)
	}
	return sub
}
