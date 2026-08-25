// Qdrant semantic-search provider.
//
// The qdrant provider runs hybrid (dense + sparse) vector search over the
// indexed paper chunks, reusing the retrieval logic that previously lived
// in internal/routes/rag.go: the embed worker (/embed, /rerank) produces
// bge-m3 dense+sparse vectors and bge-reranker-v2-m3 scores, and Qdrant
// fuses dense+sparse via RRF. Chunk-level results are collapsed to one
// Hit per paper (dedup by DOI / arXiv identity, max score kept).
//
// Failure contract: embed / Qdrant backend errors are recorded via
// BaseProvider and reported as (nil, nil), never as a non-nil error.

package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qdrant/go-client/qdrant"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

const (
	qdrantDenseName  = "dense"
	qdrantSparseName = "sparse"

	// qdrantDefaultRerankPool is the candidate-pool size fetched from
	// Qdrant before reranking (matches ragDefaultRerankPool in rag.go).
	qdrantDefaultRerankPool = 50

	qdrantEmbedTimeout  = 30 * time.Second
	qdrantQdrantTimeout = 15 * time.Second
)

// QdrantProvider searches the Qdrant chunk index semantically. It only
// answers entries carrying free text (Text or Title); identity-only
// entries are left to the exact-lookup providers.
type QdrantProvider struct {
	BaseProvider
	qdrant     *qdrant.Client
	embed      *embedClient
	collection string
}

// NewQdrantProvider mirrors newRagRetriever's config plumbing: qdrantURL
// accepts "host:port", "http://host:port" or "https://host:port" (default
// port 6334 = gRPC). The caller owns no resources; the underlying clients
// are safe for concurrent use.
func NewQdrantProvider(qdrantURL, qdrantAPIKey, collection, embedURL, embedToken string) (*QdrantProvider, error) {
	host, port, useTLS, err := parseQdrantURL(qdrantURL)
	if err != nil {
		return nil, err
	}
	cli, err := qdrant.NewClient(&qdrant.Config{
		Host:   host,
		Port:   port,
		APIKey: qdrantAPIKey,
		UseTLS: useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant client: %w", err)
	}
	return &QdrantProvider{
		qdrant:     cli,
		embed:      newEmbedClient(embedURL, embedToken),
		collection: collection,
	}, nil
}

// Name implements Provider.
func (p *QdrantProvider) Name() string { return "qdrant" }

// Search implements Provider. Semantic search needs free text: entries
// with neither Text nor Title return (nil, nil) immediately.
func (p *QdrantProvider) Search(ctx context.Context, e SearchEntry) ([]Hit, error) {
	query := e.Text
	if query == "" {
		query = e.Title
	}
	if query == "" {
		return nil, nil
	}
	topK := e.MaxResults
	if topK <= 0 {
		topK = DefaultMaxResults
	}

	// 1. Embed the query (dense + sparse).
	ectx, ecancel := context.WithTimeout(ctx, qdrantEmbedTimeout)
	defer ecancel()
	dense, sparseIdx, sparseVal, err := p.embed.embed(ectx, query, true)
	if err != nil {
		return p.RecordFailure(err)
	}

	// 2. Qdrant query — hybrid via RRF when a sparse vector is available,
	// dense-only otherwise — over a rerank-sized candidate pool.
	qctx, qcancel := context.WithTimeout(ctx, qdrantQdrantTimeout)
	defer qcancel()
	limit := uint64(qdrantDefaultRerankPool)

	var points []*qdrant.ScoredPoint
	if len(sparseIdx) > 0 {
		denseName := qdrantDenseName
		sparseName := qdrantSparseName
		points, err = p.qdrant.Query(qctx, &qdrant.QueryPoints{
			CollectionName: p.collection,
			Prefetch: []*qdrant.PrefetchQuery{
				{
					Query: qdrant.NewQueryDense(dense),
					Using: &denseName,
					Limit: qdrant.PtrOf(limit),
				},
				{
					Query: qdrant.NewQuerySparse(sparseIdx, sparseVal),
					Using: &sparseName,
					Limit: qdrant.PtrOf(limit),
				},
			},
			Query:       qdrant.NewQueryFusion(qdrant.Fusion_RRF),
			Limit:       qdrant.PtrOf(limit),
			WithPayload: qdrant.NewWithPayload(true),
		})
	} else {
		denseName := qdrantDenseName
		points, err = p.qdrant.Query(qctx, &qdrant.QueryPoints{
			CollectionName: p.collection,
			Query:          qdrant.NewQueryDense(dense),
			Using:          &denseName,
			Limit:          qdrant.PtrOf(limit),
			WithPayload:    qdrant.NewWithPayload(true),
		})
	}
	if err != nil {
		return p.RecordFailure(fmt.Errorf("qdrant query: %w", err))
	}

	// 3. Rerank the pool; on failure fall back to the fusion scores.
	points = p.rerankPoints(ctx, query, points)

	// 4. Map payloads to hits and collapse chunks of the same paper.
	return collapseQdrantHits(points, topK), nil
}

// rerankPoints re-orders points by reranker score and applies those
// scores back onto the points (mirroring ragRetriever.search). On rerank
// failure the points keep their original order and fusion scores.
func (p *QdrantProvider) rerankPoints(ctx context.Context, query string, points []*qdrant.ScoredPoint) []*qdrant.ScoredPoint {
	if len(points) <= 1 {
		return points
	}
	passages := make([]string, len(points))
	for i, pt := range points {
		passages[i] = qdrantPayloadString(pt.Payload, "chunk_text")
	}
	rctx, rcancel := context.WithTimeout(ctx, qdrantEmbedTimeout)
	defer rcancel()
	scores, err := p.embed.rerank(rctx, query, passages)
	if err != nil {
		// Don't fail the whole query: log and fall back to fusion scores.
		slog.Warn("search: qdrant rerank failed, falling back to fusion scores", "err", err)
		return points
	}
	if len(scores) != len(points) {
		return points
	}
	type rk struct {
		idx   int
		score float32
	}
	ordered := make([]rk, len(points))
	for i := range points {
		ordered[i] = rk{i, scores[i]}
	}
	// Insertion sort is fine for pool ≤ 200; simpler than custom Less.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j-1].score < ordered[j].score; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	ranked := make([]*qdrant.ScoredPoint, 0, len(points))
	for _, r := range ordered {
		pt := points[r.idx]
		// Apply the rerank score back so the hit carries the more
		// meaningful number.
		pt.Score = r.score
		ranked = append(ranked, pt)
	}
	return ranked
}

// collapseQdrantHits maps scored chunk points to paper-level hits,
// deduplicating by paper identity (DOI > arXiv id > title hash — the
// engine's identityKey) and keeping the max score per paper. The points
// are expected score-ordered, so the first occurrence of each paper
// already carries its best rank. Output is capped at limit.
func collapseQdrantHits(points []*qdrant.ScoredPoint, limit int) []Hit {
	index := map[string]int{}
	var hits []Hit
	for _, pt := range points {
		h := qdrantPointToHit(pt)
		if h.ArxivID == "" && h.DOI == "" && h.Title == "" {
			continue // chunk without any usable identity
		}
		if i, seen := index[identityKey(h)]; seen {
			m := &hits[i]
			if h.Score > m.Score {
				m.Score = h.Score
			}
			if m.Title == "" {
				m.Title = h.Title
			}
			continue
		}
		index[identityKey(h)] = len(hits)
		hits = append(hits, h)
		if len(hits) >= limit {
			break
		}
	}
	return hits
}

// qdrantPointToHit maps one Qdrant chunk payload to a paper-level hit.
// The payload carries paper identity (arxiv_id, optionally doi), title,
// authors, yymm and the chunk text; see payloadToHit in rag.go.
func qdrantPointToHit(pt *qdrant.ScoredPoint) Hit {
	pl := pt.Payload
	return Hit{
		ArxivID: registry.NormalizeArxivID(qdrantPayloadString(pl, "arxiv_id")),
		DOI:     registry.NormalizeDOI(qdrantPayloadString(pl, "doi")),
		Title:   qdrantPayloadString(pl, "title"),
		Authors: qdrantPayloadStringList(pl, "authors"),
		Year:    qdrantYYMMYear(qdrantPayloadString(pl, "yymm")),
		Score:   float64(pt.Score),
		Source:  "qdrant",
	}
}

// qdrantYYMMYear derives the publication year from the payload's 4-digit
// YYMM field (e.g. "2401" → 2024). Returns 0 when absent or malformed.
func qdrantYYMMYear(yymm string) int {
	if len(yymm) < 2 {
		return 0
	}
	yy, err := strconv.Atoi(yymm[:2])
	if err != nil {
		return 0
	}
	return 2000 + yy
}

// --- embed worker over HTTP (copied from internal/routes/rag.go) ------

type embedClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newEmbedClient(baseURL, token string) *embedClient {
	return &embedClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: qdrantEmbedTimeout},
	}
}

type embedResponse struct {
	Dense  [][]float32 `json:"dense"`
	Sparse []struct {
		Indices []uint32  `json:"indices"`
		Values  []float32 `json:"values"`
	} `json:"sparse"`
}

func (e *embedClient) embed(ctx context.Context, text string, wantSparse bool) (dense []float32, sparseIdx []uint32, sparseVal []float32, err error) {
	body, _ := json.Marshal(map[string]any{
		"texts":         []string{text},
		"return_sparse": wantSparse,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embed?lane=query", bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("content-type", "application/json")
	if e.token != "" {
		req.Header.Set("authorization", "Bearer "+e.token)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("embed worker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, nil, nil, fmt.Errorf("embed worker returned %d: %s", resp.StatusCode, raw)
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, nil, nil, fmt.Errorf("embed worker JSON: %w", err)
	}
	if len(er.Dense) == 0 {
		return nil, nil, nil, errors.New("embed worker: empty dense vec")
	}
	dense = er.Dense[0]
	if wantSparse && len(er.Sparse) > 0 {
		sparseIdx = er.Sparse[0].Indices
		sparseVal = er.Sparse[0].Values
	}
	return dense, sparseIdx, sparseVal, nil
}

type rerankResponse struct {
	Scores []float32 `json:"scores"`
}

func (e *embedClient) rerank(ctx context.Context, query string, passages []string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"query":    query,
		"passages": passages,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/rerank?lane=query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if e.token != "" {
		req.Header.Set("authorization", "Bearer "+e.token)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("rerank returned %d: %s", resp.StatusCode, raw)
	}
	var rr rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("rerank JSON: %w", err)
	}
	return rr.Scores, nil
}

// parseQdrantURL accepts "host:port", "http://host:port", "https://host:port".
// Returns host, port (default 6334 = gRPC), useTLS.
func parseQdrantURL(raw string) (string, int, bool, error) {
	raw = strings.TrimSpace(raw)
	useTLS := false
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		useTLS = u.Scheme == "https"
		raw = u.Host
	}
	host := raw
	port := 6334
	if i := strings.LastIndex(raw, ":"); i > 0 {
		host = raw[:i]
		p, err := strconv.Atoi(raw[i+1:])
		if err != nil {
			return "", 0, false, fmt.Errorf("invalid qdrant port: %q", raw[i+1:])
		}
		port = p
	}
	if host == "" {
		return "", 0, false, errors.New("qdrant URL missing host")
	}
	return host, port, useTLS, nil
}

// --- payload helpers (adapted from payloadToHit in rag.go) ------------

func qdrantPayloadString(m map[string]*qdrant.Value, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.Kind.(*qdrant.Value_StringValue); ok {
		return s.StringValue
	}
	return ""
}

func qdrantPayloadStringList(m map[string]*qdrant.Value, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	lv, ok := v.Kind.(*qdrant.Value_ListValue)
	if !ok || lv.ListValue == nil {
		return nil
	}
	out := make([]string, 0, len(lv.ListValue.Values))
	for _, item := range lv.ListValue.Values {
		if s, ok := item.Kind.(*qdrant.Value_StringValue); ok {
			out = append(out, s.StringValue)
		}
	}
	return out
}
