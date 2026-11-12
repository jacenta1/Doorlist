// Package web embeds the static files so the binary is self-contained.
package web

import "embed"

//go:embed static
var Static embed.FS
