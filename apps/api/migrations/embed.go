// Package migrations embeds the goose SQL files so cmd/migrate ships them in the image.
package migrations

import "embed"

// FS holds both directories: "." for erp_migrator migrations and "superuser" for
// the admin-only ones (event triggers).
//
//go:embed *.sql superuser/*.sql
var FS embed.FS
