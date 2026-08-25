// Package migrations embeds the goose SQL migrations into the binary so a
// deploy is a single static file: no .sql to copy to the Raspberry Pi.
package migrations

import "embed"

// FS holds every migration in this directory. goose reads it through
// goose.SetBaseFS, and the files live at the root of the FS. The pattern
// starts with a digit on purpose: it embeds the numbered migrations and
// nothing else (macOS "._" sidecar files on exFAT volumes, for instance).
//
//go:embed 0*.sql
var FS embed.FS
