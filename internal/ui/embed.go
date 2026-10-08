// Package ui holds the built web UI. Rebuild it with `npm run build` in ui/.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func FS() fs.FS {
	f, _ := fs.Sub(dist, "dist")
	return f
}
