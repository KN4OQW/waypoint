// Package ui embeds the dashboard's static assets into the daemon binary,
// keeping deployment a single artifact.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS returns the dashboard filesystem rooted at the asset directory.
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	return sub
}
