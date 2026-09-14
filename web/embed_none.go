//go:build !embedui

package web

import "io/fs"

func embeddedFS() (fs.FS, error) { return nil, nil }
