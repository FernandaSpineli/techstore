// Package web embeds the demo storefront: one HTML page plus static assets,
// served by the API binary itself so the whole flow runs from one origin.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html assets
var files embed.FS

// contentSecurityPolicy only allows this origin's scripts and API, plus
// Google Fonts. No inline script or style is permitted.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

var index = func() []byte {
	b, err := files.ReadFile("index.html")
	if err != nil {
		panic(err) // the file is embedded at build time
	}
	return b
}()

// Page serves the storefront's HTML. It is mounted on every path a user can
// land on: the root, the password-reset link and Stripe's redirects.
func Page(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

// Assets serves /assets/*.
func Assets() http.Handler {
	sub, err := fs.Sub(files, "assets")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/assets/", http.FileServerFS(sub))
}
