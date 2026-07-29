// Package ui embeds the dashboard's static assets into the daemon binary,
// keeping deployment a single artifact.
package ui

import (
	"embed"
	"io/fs"
)

// static/locales/index.json is derived from the catalogs beside it, so it is
// generated rather than maintained. See tools/genlocaleindex.
//go:generate go run ../tools/genlocaleindex -dir static/locales

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
