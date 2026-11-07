// Package migrations embeds the SQL migrations so the binary can apply them itself
// (`doexam migrate up`) without the migrate CLI or a migrations folder next to it.
package migrations

import "embed"

// FS holds every NNNNNN_name.up.sql / .down.sql file of this directory.
//
//go:embed *.sql
var FS embed.FS
