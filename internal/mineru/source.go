package mineru

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// SourceCatalog is the durable source/revision contract. No legacy output
// pointer participates in readiness. Production *registry.Store satisfies it.
type SourceCatalog interface {
	GetPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	GetReadyParseBundle(context.Context, objstore.Store, string, string) (registry.ParseBundle, bool, error)
	PublishBundle(context.Context, objstore.Store, registry.ParseRevision, bool) (registry.ParseBundle, error)
	ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error)
	ListPaperSources(context.Context, string) ([]registry.PaperSource, error)
	RegisterFrozenPaperSource(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	RegisterFrozenPDF(context.Context, objstore.Store, string, string, []byte) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
}

// SourceCatalog exposes the configured durable catalog seam for unified
// routes. It is immutable after construction; nil converters have no catalog.
func (c *Converter) SourceCatalog() SourceCatalog {
	if c == nil {
		return nil
	}
	return c.sources
}

func cloneJob(j *Job) Job {
	cp := *j
	if j.Fetch != nil {
		v := *j.Fetch
		cp.Fetch = &v
	}
	if j.Convert != nil {
		v := *j.Convert
		cp.Convert = &v
	}
	if j.Queue != nil {
		v := *j.Queue
		cp.Queue = &v
	}
	return cp
}

func sourceAlias(paperID, sourceID string) string { return "source:" + paperID + ":" + sourceID }
func sourceJobKey(src registry.PaperSource) string {
	return sourceAlias(src.PaperID, src.SourceID) + ":" + src.Sha256
}

// EnsureFrozenSource is PDF-only: it copies a legacy source once, verifies the
// authoritative frozen bytes, and NEVER submits inference or requires a token.
// A frozen source with missing/corrupt content fails rather than falling back.
func (c *Converter) EnsureFrozenSource(ctx context.Context, paperID, sourceID string) (registry.PaperSource, error) {
	if !c.cfg.PaperAccessEnabled {
		return registry.PaperSource{}, &Error{Msg: "paper access disabled", Kind: ErrFatal}
	}
	if c.sources == nil {
		return registry.PaperSource{}, registry.ErrCatalogUnavailable
	}
	src, found, err := c.sources.GetPaperSource(ctx, paperID, sourceID)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if !found {
		return registry.PaperSource{}, objstore.ErrNotFound
	}
	src, err = c.sources.FreezePaperSource(ctx, c.store, src)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if src.PaperID != paperID || src.SourceID != sourceID || src.ObjstoreKey != paperbundle.PDFKey(paperID, sourceID) {
		return registry.PaperSource{}, paperbundle.ErrIntegrity
	}
	return src, nil
}

// LookupSource returns only a process job snapshot, not a readiness assertion.
func (c *Converter) LookupSource(paperID, sourceID string) (*Job, bool) {
	return c.Lookup(sourceAlias(paperID, sourceID))
}

func sourceFailure(paperID, sourceID string, err error) *Job {
	j := storageReadFailure(sourceID, err)
	j.PaperID = paperID
	j.SourceID = sourceID
	if errors.Is(err, ErrFatal) || errors.Is(err, ErrNoDOISource) || errors.Is(err, objstore.ErrNotFound) || errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, paperbundle.ErrInvalid) {
		j.ErrKind = ErrFatal
		j.Err = err // Logical/data failures are not storage outages.
	}
	return j
}

// EnsureSource lazily reparses precisely the selected frozen PDF. A cache hit
// requires the exact source hash and a verified complete manifest. Every repair
// mints a NEW revision; a stale process Done record is never a cache hit.
func (c *Converter) EnsureSource(ctx context.Context, paperID, sourceID string) *Job {
	if !c.cfg.PaperAccessEnabled {
		return sourceFailure(paperID, sourceID, &Error{Msg: "paper access disabled", Kind: ErrFatal})
	}
	readCtx, cancel := objstore.ReadContext(ctx)
	src, err := c.EnsureFrozenSource(readCtx, paperID, sourceID)
	if err != nil {
		cancel()
		return sourceFailure(paperID, sourceID, err)
	}
	ready, found, err := c.sources.GetReadyParseBundle(readCtx, c.store, paperID, sourceID)
	cancel()
	if err != nil {
		return sourceFailure(paperID, sourceID, err)
	}
	if found && ready.SourceID == src.SourceID && ready.SourcePDFSHA256 == src.Sha256 {
		c.counters.CacheHits.Add(1)
		job := &Job{Canonical: sourceID, PaperID: paperID, SourceID: sourceID, SourcePDFSHA256: src.Sha256, RevisionID: ready.RevisionID, State: JobStateDone, Phase: PhaseReady, FinishedAt: c.now()}
		key := sourceJobKey(src)
		c.mu.Lock()
		c.aliases[sourceAlias(paperID, sourceID)] = key
		// Cache snapshots support async status, not future readiness checks.
		// Never replace an active local worker with another edge's revision.
		if existing := c.jobs[key]; existing == nil || existing.State != JobStateQueued && existing.State != JobStateRunning {
			cp := cloneJob(job)
			c.jobs[key] = &cp
		}
		c.mu.Unlock()
		return job
	}
	c.counters.CacheMisses.Add(1)
	if !c.Enabled() {
		return sourceFailure(paperID, sourceID, &Error{Msg: c.DisabledReason(), Kind: ErrFatal})
	}
	key := sourceJobKey(src)
	c.mu.Lock()
	c.aliases[sourceAlias(paperID, sourceID)] = key
	if existing, ok := c.jobs[key]; ok {
		if existing.State == JobStateQueued || existing.State == JobStateRunning || existing.State == JobStateFailed && c.now().Before(existing.CooldownUntil) {
			cp := cloneJob(existing)
			c.mu.Unlock()
			return &cp
		}
	}
	job := &Job{Canonical: sourceID, PaperID: paperID, SourceID: sourceID, SourcePDFSHA256: src.Sha256, RevisionID: registry.NewParseRevisionID(), State: JobStateQueued, SubmittedAt: c.now()}
	c.jobs[key] = job
	cp := *job
	c.mu.Unlock()
	go c.runJob(key, "source_id", sourceID, func(ctx context.Context) error { return c.convertSource(ctx, key, src, cp.RevisionID) })
	return &cp
}

func (c *Converter) convertSource(ctx context.Context, key string, src registry.PaperSource, revisionID string) error {
	// Re-verify the actual source just before upload. Its row/hash cannot be
	// substituted by a mutable canonical asset after Ensure returned 202.
	pdf, err := paperbundle.New(c.store).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
	if err != nil {
		return fmt.Errorf("read frozen PDF: %w", err)
	}
	c.transition(key, func(j *Job) {
		j.Phase = PhaseConvertingMD
		j.Convert = &ConvertProgress{StartedAt: c.now(), Stage: "submitting"}
	})
	for {
		client, slot, ok := c.keyRing.Acquire()
		if !ok {
			return &Error{Msg: fmt.Sprintf("all %d MinerU API keys have exhausted today's daily quota", c.keyRing.Size()), Kind: ErrDailyLimit}
		}
		err = c.convertSourceWithClient(ctx, client, key, src, revisionID, pdf)
		if errors.Is(err, ErrDailyLimit) {
			c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
			c.logger.Warn("mineru V1: exhausted token, rotating", "slot", slot)
			continue
		}
		return err
	}
}

func (c *Converter) convertSourceWithClient(ctx context.Context, client *Client, key string, src registry.PaperSource, revisionID string, pdf []byte) error {
	fileID, err := client.UploadV1(ctx, src.SourceID+".pdf", pdf, src.Sha256)
	if err != nil {
		return err
	}
	job, err := client.SubmitV1(ctx, fileID, c.cfg.MinerUTier, c.cfg.MinerUIsOCR)
	if err != nil {
		return err
	}
	c.transition(key, func(j *Job) { j.Convert.MinerUTaskID = job.JobID; j.Convert.Stage = "running" })
	for {
		outputID, done, err := job.output()
		if err != nil {
			return err
		}
		if done {
			c.transition(key, func(j *Job) { j.Convert.Stage = "downloading_zip" })
			result, err := client.FetchV1Result(ctx, outputID)
			if err != nil {
				return err
			}
			c.transition(key, func(j *Job) { j.Convert.Stage = "persisting_bundle" })
			_, err = paperbundle.New(c.store).WriteBundle(ctx, paperbundle.Input{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: revisionID, SourcePDFSHA256: src.Sha256, Files: result.Members, MiddlePath: result.MiddlePath, MarkdownPath: result.MarkdownPath})
			if err != nil {
				return fmt.Errorf("write complete bundle: %w", err)
			}
			_, err = c.sources.PublishBundle(ctx, c.store, registry.ParseRevision{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: revisionID, Schema: MiddleSchema, SchemaVersion: MiddleSchemaVersion, Tier: c.cfg.MinerUTier}, true)
			if err != nil {
				return fmt.Errorf("publish complete bundle: %w", err)
			}
			c.transition(key, func(j *Job) { j.Convert.CompletedAt = c.now() })
			if c.cfg.IndexPusher != nil {
				if err := c.cfg.IndexPusher.PushIndex(ctx, src.PaperID); err != nil {
					c.logger.Warn("mineru: index push failed", "paper_id", src.PaperID, "error", err)
				}
			}
			return nil
		}
		timer := time.NewTimer(c.cfg.MinerUPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		job, err = client.GetV1Job(ctx, job.JobID)
		if err != nil {
			return err
		}
		c.transition(key, func(j *Job) { j.Convert.PolledCount++ })
	}
}
