package registry

import "context"

// AdoptPendingDownloadRequests transfers legacy pending papers into the single
// durable downloader admission owner. Existing journal rows (including terminal
// rows) are never reset. Excluding them BEFORE LIMIT prevents an old queued batch
// from starving pending papers that have never been admitted.
//
// Called by recovery rather than startup construction so an asynchronously
// migrating/unavailable catalog is retried instead of silently losing adoption.
func (s *Store) AdoptPendingDownloadRequests(ctx context.Context, limit int) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	if limit < 1 || limit > 128 {
		limit = 128
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO downloader_requests (paper_id,request_id,input,kind,ref)
SELECT p.paper_id,gen_random_uuid()::text,
       coalesce(nullif(p.arxiv_id,''),p.doi),
       CASE WHEN coalesce(p.arxiv_id,'')<>'' THEN 'arxiv' ELSE 'doi' END,
       jsonb_build_object('ArxivID',coalesce(p.arxiv_id,''),
                          'DOI',coalesce(p.doi,''),
                          'OpenAlexID',coalesce(p.openalex_id,''),
                          'Title',coalesce(p.title,''),
                          'Authors',coalesce(p.authors,'{}'::text[]))
FROM papers p
WHERE p.status='pending'
  AND (coalesce(p.arxiv_id,'')<>'' OR coalesce(p.doi,'')<>'')
  AND NOT EXISTS (SELECT 1 FROM downloader_requests r WHERE r.paper_id=p.paper_id)
ORDER BY p.paper_id
LIMIT $1
ON CONFLICT (paper_id) DO NOTHING`, limit)
	return err
}
