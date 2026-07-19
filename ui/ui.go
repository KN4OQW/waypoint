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

// AuthPage returns the self-contained pre-auth screen (the claim/login page,
// RFC-0002). The auth subsystem serves it at the top-level route before a session
// exists — it is the one asset reachable pre-auth, so it inlines its own CSS and
// JS and pulls in no gated sub-resources. The daemon hands it to auth.Options.
func AuthPage() []byte {
	b, err := static.ReadFile("static/auth.html")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	return b
}
