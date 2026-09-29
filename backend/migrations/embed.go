// Package migrations embeds the SQL schema files so the backend can apply
// them itself instead of relying on the Postgres init directory.
package migrations

import "embed"

// FS holds every *.sql migration, applied in lexical order.
//
//go:embed *.sql
var FS embed.FS
