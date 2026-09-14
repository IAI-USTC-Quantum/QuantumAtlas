//go:build !embedui

package web

import (
	"context"
	"testing"
)

func TestSourceBuildHasNoImplicitLocalDist(t *testing.T) {
	tree, err := embeddedFS()
	if err != nil || tree != nil {
		t.Fatal("ordinary build must not depend on local dist")
	}
	if _, err := Resolve(context.Background(), "dev"); err == nil {
		t.Fatal("dev build should request an explicit tagged install or embedui build")
	}
}
