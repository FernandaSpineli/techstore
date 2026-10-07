// Package migrations embeds the SQL schema migrations into the binary, so a
// deploy never depends on files present on the host.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.sql
var FS embed.FS
