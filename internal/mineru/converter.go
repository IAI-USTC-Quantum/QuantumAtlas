package mineru

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"
)

// Job state constants.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// failCooldown is how long a failed conversion is remembered before a new
// GET is allowed to retry. Prevents hammering MinerU (and burning quota)
// on a permanently-bad PDF, while still letting transient failures recover
// after a short wait.
const failCooldown = 60 * time.Second

// Job is the in-process record of one paper's conversion attempt.
//
// Jobs are value-copied out of the Converter under lock (see snapshot) so
// callers never race on the live fields.
type Job struct {
	ArxivID    string
	State      string
	StartedAt  time.Time
	FinishedAt time.Time
	// Err holds the failure detail when State == StateFailed.
	Err string
	// MDKey is the object key the markdown was written to on success.
	MDKey string
	// ImageCount is how many images were uploaded alongside the markdown.
	ImageCount int
}

// Converter orchestrates server-side silent markdown conversion. One
// instance is shared process-wide; it dedupes concurrent requests for the
// same paper so MinerU quota is only spent once per (canonical) arxiv id.
//
// Dedup is in-process only. Across the two production edges (RackNerd +
// Alibaba) that share one RustFS bucket, both could in principle start a
// conversion for the same paper — but each checks the store for existing
// markdown before submitting, and conditional create-only PUTs make the
// upload itself idempotent, so the worst case is a rare double MinerU
// submission, never a corrupted store.
type Converter struct {
	cfg        *config.Config
	store      objstore.Store
	shareStore *shares.Store
	client     *Client

	mu   sync.Mutex
	jobs map[string]*Job
}

// NewConverter builds a Converter. It is always constructed; whether it
// can actually convert is reported by Enabled (false when no MinerU token
// is configured). store and shareStore must be non-nil.
func NewConverter(cfg *config.Config, store objstore.Store, shareStore *shares.Store) *Converter {
	return &Converter{
		cfg:        cfg,
		store:      store,
		shareStore: shareStore,
		client:     NewClient(cfg.MinerUAPIToken, cfg.MinerUAPIBaseURL, nil),
		jobs:       map[string]*Job{},
	}
}

// Enabled reports whether server-side conversion is configured.
func (c *Converter) Enabled() bool {
	return c.cfg != nil && c.cfg.MinerUEnabled()
}

// Lookup returns a snapshot of the current job for canonical, or nil when
// none is tracked.
func (c *Converter) Lookup(canonical string) *Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[canonical]; ok {
		snap := *j
		return &snap
	}
	return nil
}

// Ensure returns a snapshot of the conversion job for canonical, starting
// a fresh background conversion when appropriate:
//
//   - queued / running  → returns the in-flight job unchanged.
//   - done              → returns the finished job (caller re-checks the
//     store for the now-cached markdown).
//   - failed within the cooldown window → returns the failed job (caller
//     surfaces the error; no retry yet).
//   - absent, or failed past the cooldown → starts a new job and returns
//     it in the queued state.
//
// The conversion runs on a detached context (not the request's) so the
// 202 response can return immediately without cancelling the work.
func (c *Converter) Ensure(canonical string) *Job {
	c.mu.Lock()
	defer c.mu.Unlock()

	if j, ok := c.jobs[canonical]; ok {
		switch j.State {
		case StateQueued, StateRunning, StateDone:
			snap := *j
			return &snap
		case StateFailed:
			if time.Since(j.FinishedAt) < failCooldown {
				snap := *j
				return &snap
			}
			// past cooldown → fall through and start a new attempt.
		}
	}

	job := &Job{
		ArxivID:   canonical,
		State:     StateQueued,
		StartedAt: time.Now().UTC(),
	}
	c.jobs[canonical] = job
	go c.run(canonical)

	snap := *job
	return &snap
}

// run performs the actual conversion in the background. It is launched
// once per job by Ensure.
func (c *Converter) run(canonical string) {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(c.cfg.MinerUTimeout)*time.Second+30*time.Second)
	defer cancel()

	c.setState(canonical, StateRunning, "")
	slog.Info("mineru: server-side conversion started", "arxiv_id", canonical)

	mdKey, imgCount, err := c.convert(ctx, canonical)
	if err != nil {
		c.finish(canonical, StateFailed, err.Error(), "", 0)
		slog.Warn("mineru: server-side conversion failed", "arxiv_id", canonical, "error", err)
		return
	}
	c.finish(canonical, StateDone, "", mdKey, imgCount)
	slog.Info("mineru: server-side conversion done",
		"arxiv_id", canonical, "md_key", mdKey, "images", imgCount)
}

// convert is the conversion pipeline: resolve PDF → build share URL →
// submit to MinerU → poll → fetch result → upload markdown + images.
func (c *Converter) convert(ctx context.Context, canonical string) (mdKey string, imgCount int, err error) {
	// 1. PDF must exist in the store.
	pdfKey, err := c.resolvePDFKey(ctx, canonical)
	if err != nil {
		return "", 0, err
	}

	// 2. Build the public PDF URL MinerU will fetch.
	pdfURL, err := c.buildPDFURL(canonical, pdfKey)
	if err != nil {
		return "", 0, err
	}

	// 3. Submit + poll.
	taskID, err := c.client.SubmitURLTask(ctx, pdfURL, SubmitOptions{
		ModelVersion:  c.cfg.MinerUModelVersion,
		Language:      c.cfg.MinerULanguage,
		EnableFormula: c.cfg.MinerUEnableFormula,
		EnableTable:   c.cfg.MinerUEnableTable,
		IsOCR:         c.cfg.MinerUIsOCR,
	})
	if err != nil {
		return "", 0, fmt.Errorf("submit task: %w", err)
	}

	zipURL, err := c.poll(ctx, taskID)
	if err != nil {
		return "", 0, err
	}

	// 4. Download + extract.
	result, err := c.client.FetchResult(ctx, zipURL)
	if err != nil {
		return "", 0, fmt.Errorf("fetch result: %w", err)
	}

	// 5. Upload images first, then markdown last. Markdown is the
	// completion marker callers poll on (and the page that links to the
	// images), so writing every image before it guarantees that once the
	// markdown object exists, all of its referenced images are already
	// stored — no broken-link / racing-reader window.
	imagesBase := paperassets.AssetKey("images", canonical)
	for rel, data := range result.Images {
		// rel is e.g. "images/abc.jpg"; strip the leading "images/" so we
		// don't double it under the images key prefix.
		name := strings.TrimPrefix(rel, "images/")
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		imgKey := imagesBase + "/" + name
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := c.putBytes(ctx, imgKey, data, ct); err != nil {
			return "", 0, fmt.Errorf("upload image %s: %w", name, err)
		}
		imgCount++
	}

	mdKey = paperassets.AssetKey("markdown", canonical)
	if err := c.putBytes(ctx, mdKey, result.Markdown, "text/markdown; charset=utf-8"); err != nil {
		return "", 0, fmt.Errorf("upload markdown: %w", err)
	}
	return mdKey, imgCount, nil
}

// poll loops GetTask until the task is done (returns full_zip_url), failed,
// or the deadline / context expires.
func (c *Converter) poll(ctx context.Context, taskID string) (string, error) {
	interval := time.Duration(c.cfg.MinerUPollInterval * float64(time.Second))
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(time.Duration(c.cfg.MinerUTimeout) * time.Second)
	for {
		st, err := c.client.GetTask(ctx, taskID)
		if err != nil {
			return "", fmt.Errorf("poll task: %w", err)
		}
		switch st.State {
		case "done":
			if st.FullZipURL == "" {
				return "", &Error{Msg: "task done but no full_zip_url"}
			}
			return st.FullZipURL, nil
		case "failed":
			msg := st.ErrMsg
			if msg == "" {
				msg = "task failed"
			}
			return "", &Error{Msg: "task failed: " + msg}
		}
		if time.Now().After(deadline) {
			return "", &Error{Msg: fmt.Sprintf("task did not finish within MINERU_TIMEOUT=%ds", c.cfg.MinerUTimeout)}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// resolvePDFKey returns the object key of the paper's PDF, or an error
// when no PDF is present.
func (c *Converter) resolvePDFKey(ctx context.Context, canonical string) (string, error) {
	pdfKey := paperassets.AssetKey("pdf", canonical)
	if _, exists, err := c.store.Stat(ctx, pdfKey); err == nil && exists {
		return pdfKey, nil
	}
	resolved := paperassets.ResolveAssetsViaStore(ctx, c.store, canonical)
	if resolved.PDFPath != "" {
		return resolved.PDFPath, nil
	}
	return "", &Error{Msg: "no PDF in raw storage; upload it first via /api/papers/{arxiv_id}/upload-pdf"}
}

// buildPDFURL mints the public share URL for the PDF, mirroring the
// mineru-claim handler: prefer a static QATLAS_SHARE_ACCESS_TOKEN, else
// create a per-asset share record.
func (c *Converter) buildPDFURL(canonical, pdfKey string) (string, error) {
	relSharePath := paperassets.ShareRelPathForKey(pdfKey)
	shareToken := c.cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = c.cfg.PublicBaseURL
	} else {
		rec, err := shares.CreateRecord(c.shareStore, c.cfg, shares.CreateOptions{
			Paths: []string{relSharePath},
			Label: "mineru pdf (server-side): " + canonical,
		}, c.store)
		if err != nil {
			return "", fmt.Errorf("build share URL: %w", err)
		}
		shareToken = rec.Token
	}
	return shares.BuildURL(shareToken, relSharePath, shareBaseURL), nil
}

// putBytes writes data to key with a create-only conditional PUT, falling
// back to an unconditional PUT when the backend doesn't support
// preconditions. An existing object (412 / precondition failed) is treated
// as success: the content is already cached.
func (c *Converter) putBytes(ctx context.Context, key string, data []byte, contentType string) error {
	sum := sha256.Sum256(data)
	meta := map[string]string{"sha256": hex.EncodeToString(sum[:])}
	_, err := c.store.PutWithOptions(ctx, key, bytes.NewReader(data), int64(len(data)), objstore.PutOptions{
		ContentType: contentType,
		Metadata:    meta,
		IfNoneMatch: "*",
	})
	if err == nil {
		return nil
	}
	if objstore.IsPreconditionFailed(err) {
		// Already present (e.g. the other edge raced us, or a contributor
		// uploaded in the meantime) — the cache is warm, nothing to do.
		return nil
	}
	if objstore.IsPreconditionUnsupported(err) {
		_, err = c.store.PutWithOptions(ctx, key, bytes.NewReader(data), int64(len(data)), objstore.PutOptions{
			ContentType: contentType,
			Metadata:    meta,
		})
	}
	return err
}

func (c *Converter) setState(canonical, state, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[canonical]; ok {
		j.State = state
		j.Err = errMsg
	}
}

func (c *Converter) finish(canonical, state, errMsg, mdKey string, imgCount int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[canonical]; ok {
		j.State = state
		j.Err = errMsg
		j.MDKey = mdKey
		j.ImageCount = imgCount
		j.FinishedAt = time.Now().UTC()
	}
}
