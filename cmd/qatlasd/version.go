package main

import (
	"runtime/debug"
	"strings"
)

// version is the standard GoReleaser ldflags target. Do not override its
// default flags just to populate additional unused build metadata.
var version = "dev"

// Version is resolved before main's dependency-free --version branch and is
// shared by CLI, API, response headers and upstream user agents.
var Version = resolvedVersion()

func resolvedVersion() string {
	module := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		module = info.Main.Version
	}
	return resolveVersion(version, module)
}

func resolveVersion(injected, module string) string {
	for _, value := range []string{injected, module} {
		value = strings.TrimSpace(value)
		if value != "" && value != "dev" && value != "(devel)" {
			return strings.TrimPrefix(value, "v")
		}
	}
	return "dev"
}
