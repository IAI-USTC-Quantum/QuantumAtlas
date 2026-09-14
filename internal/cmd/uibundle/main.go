// Command uibundle packages an already built full web/dist tree for Release.
// It never runs npm/Sphinx and never commits generated resources.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/web"
)

func main() {
	version := flag.String("version", "", "exact release version (required)")
	source := flag.String("source", "web/dist", "complete built UI tree")
	output := flag.String("output", "build/ui", "output directory")
	flag.Parse()
	v, err := web.ReleaseVersion(*version)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*output, 0755); err != nil {
		fail(err)
	}
	f, err := os.CreateTemp(*output, ".ui-*.zip")
	if err != nil {
		fail(err)
	}
	name := f.Name()
	err = web.Pack(os.DirFS(*source), v, f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		info, statErr := os.Stat(name)
		err = statErr
		if err == nil && info.Size() > 64<<20 {
			err = fmt.Errorf("UI ZIP exceeds 64 MiB download limit")
		}
	}
	dest := filepath.Join(*output, web.BundleName(v))
	if err == nil {
		err = os.Rename(name, dest)
	}
	if err != nil {
		os.Remove(name)
		fail(err)
	}
	fmt.Println(dest)
}

func fail(err error) { fmt.Fprintln(os.Stderr, "uibundle:", err); os.Exit(1) }
