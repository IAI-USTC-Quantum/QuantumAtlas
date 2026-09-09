package main

import (
	"context"
	"sync"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
)

// downloadBackground owns both fleet maintenance and admission recovery. Closing
// database pools or unlocking the spool before these goroutines exit is unsafe.
type downloadBackground struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func startDownloadBackground(tasks ...func(context.Context)) *downloadBackground {
	ctx, cancel := context.WithCancel(context.Background())
	b := &downloadBackground{cancel: cancel, done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(task func(context.Context)) {
			defer wg.Done()
			task(ctx)
		}(task)
	}
	go func() {
		wg.Wait()
		close(b.done)
	}()
	return b
}

func (b *downloadBackground) Stop() {
	if b == nil {
		return
	}
	b.once.Do(b.cancel)
	<-b.done
}

func legacyDownloadRecoveryEnabled(cfg *config.Config) bool {
	return cfg.PaperAccessEnabled && !cfg.DownloaderRemoteEnabled
}
