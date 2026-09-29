// Package db embeds the SQL migrations so the binary can migrate itself.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
