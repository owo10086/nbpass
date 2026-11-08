package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var built embed.FS

var Assets = assets()

func assets() fs.FS {
	assets, err := fs.Sub(built, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
