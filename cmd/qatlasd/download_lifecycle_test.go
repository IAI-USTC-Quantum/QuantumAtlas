package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
)

func TestDownloadLifecycleStopCancelsAndJoins(t *testing.T) {
	cancelled := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	loop := func(ctx context.Context) {
		<-ctx.Done()
		cancelled <- struct{}{}
		<-release
	}
	background := startDownloadBackground(loop, loop)
	stopped := make(chan struct{})
	go func() {
		background.Stop()
		close(stopped)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("background loop was not cancelled")
		}
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before background loops exited")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish joining")
	}
	background.Stop() // repeated stop is safe
	var disabled *downloadBackground
	disabled.Stop()
}

func TestDownloadLifecycleSingleRecoveryOwner(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		paperAccess, fleet, legacy bool
	}{
		{"disabled", false, false, false},
		{"legacy", true, false, true},
		{"fleet-owns-pending", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{PaperAccessEnabled: tc.paperAccess, DownloaderRemoteEnabled: tc.fleet}
			if got := legacyDownloadRecoveryEnabled(cfg); got != tc.legacy {
				t.Fatalf("legacy recovery = %v, want %v", got, tc.legacy)
			}
		})
	}
}
