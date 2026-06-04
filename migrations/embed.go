// Package migrations embeds the SQL migration files so the server binary can
// apply them with the `migrate` subcommand regardless of the working directory.
package migrations

import "embed"

// FS holds all *.sql migration files in this directory.
//
//go:embed *.sql
var FS embed.FS
