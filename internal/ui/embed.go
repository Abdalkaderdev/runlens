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
