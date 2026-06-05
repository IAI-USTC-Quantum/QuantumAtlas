// Package routes — RAG semantic-search handler.
//
// /api/rag/* implements hybrid (dense + sparse) vector search over the
// indexed arXiv paper chunks. Two endpoints:
//
//	POST /api/rag/search    — gated by authGuard + scopeGuard("papers", "read")
//	GET  /api/rag/healthz   — anonymous (intentionally coarse status)
//
// Both routes are registered iff ALL of the following hold:
//
//   - cfg.PaperAccessEnabled = true (master switch — RAG hits return
//     paper-derivative bytes, same as /api/papers/{id}/markdown)
//   - cfg.RAGQdrantURL != ""        (operator pointed us at a Qdrant)
//   - cfg.RAGEmbedURL  != ""        (operator pointed us at an embed worker)
//
// When any is false the routes are absent and any request returns 404 —
// indistinguishable from "no such handler". This preserves the public
// posture of quantum-atlas.ai (master switch OFF, "server does not
// redistribute markdown bytes").
//
// Architecture (v0.20.0+):
//
//	browser → qatlasd (this handler)
//	             │ gRPC :6334
//	             ├──→ Qdrant (collection: cfg.RAGQdrantCollection)
//	             │ HTTP
//	             └──→ embed worker /embed and /rerank
//	                  (the only Python piece still left;
//	                   lives in rag/qatlas_rag/embed/)
//
// The embed worker is GPU-resident (bge-m3 + bge-reranker-v2-m3).
// Everything else — query parsing, hybrid fusion, rerank coordination,
// snippet assembly — happens here in Go.

package routes

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
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/qdrant/go-client/qdrant"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
)

const (
	ragDenseName  = "dense"
	ragSparseName = "sparse"

	ragDefaultTopK       = 8
	ragDefaultRerankPool = 50
	ragMaxTopK           = 50
	ragMaxRerankPool     = 200

	ragEmbedTimeout  = 30 * time.Second
	ragQdrantTimeout = 15 * time.Second
)

// --- request / response shapes (kept compatible with the previous
// Python sidecar so the SPA frontend doesn't change) ----------------

type ragSearchRequest struct {
	Query      string            `json:"query"`
	TopK       int               `json:"top_k"`
	Rerank     *bool             `json:"rerank,omitempty"`
	RerankPool int               `json:"rerank_pool"`
	UseSparse  *bool             `json:"use_sparse,omitempty"`
	Filters    map[string]string `json:"filters,omitempty"`
}

type ragSearchHit struct {
	ArxivID     string   `json:"arxiv_id"`
	Canonical   string   `json:"canonical"`
	YYMM        string   `json:"yymm"`
	Version     int      `json:"version"`
	Title       *string  `json:"title"`
	Authors     []string `json:"authors"`
	Categories  []string `json:"categories"`
	SectionPath []string `json:"section_path"`
	ChunkIndex  int      `json:"chunk_index"`
	Snippet     string   `json:"snippet"`
	Score       float32  `json:"score"`
	MDObjectKey string   `json:"md_object_key"`
	CharStart   int      `json:"char_start"`
	CharEnd     int      `json:"char_end"`
	ImageRefs   []string `json:"image_refs"`
}

type ragSearchResponse struct {
	Query    string         `json:"query"`
	TookS    float64        `json:"took_s"`
	Reranked bool           `json:"reranked"`
	Results  []ragSearchHit `json:"results"`
}

type ragHealthResponse struct {
	Status string `json:"status"` // "ok" | "degraded" | "down"
}

// --- embed worker over HTTP -----------------------------------------

type embedClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newEmbedClient(baseURL, token string) *embedClient {
	return &embedClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: ragEmbedTimeout},
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

func (e *embedClient) healthz(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("embed /healthz: %d", resp.StatusCode)
	}
	return nil
}

// --- retriever (Qdrant client + embed client) -----------------------

type ragRetriever struct {
	qdrant     *qdrant.Client
	embed      *embedClient
	collection string
}

func newRagRetriever(cfg *config.Config) (*ragRetriever, error) {
	host, port, useTLS, err := parseQdrantURL(cfg.RAGQdrantURL)
	if err != nil {
		return nil, err
	}
	cli, err := qdrant.NewClient(&qdrant.Config{
		Host:   host,
		Port:   port,
		APIKey: cfg.RAGQdrantAPIKey,
		UseTLS: useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant client: %w", err)
	}
	return &ragRetriever{
		qdrant:     cli,
		embed:      newEmbedClient(cfg.RAGEmbedURL, cfg.RAGEmbedToken),
		collection: cfg.RAGQdrantCollection,
	}, nil
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

func (r *ragRetriever) health(ctx context.Context) ragHealthResponse {
	qctx, qcancel := context.WithTimeout(ctx, 3*time.Second)
	defer qcancel()
	_, qerr := r.qdrant.HealthCheck(qctx)

	ectx, ecancel := context.WithTimeout(ctx, 3*time.Second)
	defer ecancel()
	eerr := r.embed.healthz(ectx)

	switch {
	case qerr == nil && eerr == nil:
		return ragHealthResponse{Status: "ok"}
	case qerr == nil || eerr == nil:
		return ragHealthResponse{Status: "degraded"}
	default:
		return ragHealthResponse{Status: "down"}
	}
}

func (r *ragRetriever) search(ctx context.Context, req ragSearchRequest) (ragSearchResponse, error) {
	t0 := time.Now()

	// defaults
	if req.TopK <= 0 {
		req.TopK = ragDefaultTopK
	}
	if req.TopK > ragMaxTopK {
		req.TopK = ragMaxTopK
	}
	if req.RerankPool <= 0 {
		req.RerankPool = ragDefaultRerankPool
	}
	if req.RerankPool > ragMaxRerankPool {
		req.RerankPool = ragMaxRerankPool
	}
	useRerank := req.Rerank == nil || *req.Rerank
	useSparse := req.UseSparse == nil || *req.UseSparse

	// 1. embed query (optionally with sparse)
	ectx, ecancel := context.WithTimeout(ctx, ragEmbedTimeout)
	defer ecancel()
	dense, sparseIdx, sparseVal, err := r.embed.embed(ectx, req.Query, useSparse)
	if err != nil {
		return ragSearchResponse{}, err
	}

	// 2. Qdrant query — hybrid when sparse vector is available, dense-only otherwise.
	qctx, qcancel := context.WithTimeout(ctx, ragQdrantTimeout)
	defer qcancel()

	pool := req.TopK
	if useRerank {
		pool = req.RerankPool
	}
	limit := uint64(pool)

	filter := buildRagFilter(req.Filters)

	var points []*qdrant.ScoredPoint
	hasSparse := useSparse && len(sparseIdx) > 0
	if hasSparse {
		// Hybrid via RRF: prefetch dense + sparse, fuse, return pool.
		denseName := ragDenseName
		sparseName := ragSparseName
		points, err = r.qdrant.Query(qctx, &qdrant.QueryPoints{
			CollectionName: r.collection,
			Prefetch: []*qdrant.PrefetchQuery{
				{
					Query:  qdrant.NewQueryDense(dense),
					Using:  &denseName,
					Limit:  qdrant.PtrOf(limit),
					Filter: filter,
				},
				{
					Query:  qdrant.NewQuerySparse(sparseIdx, sparseVal),
					Using:  &sparseName,
					Limit:  qdrant.PtrOf(limit),
					Filter: filter,
				},
			},
			Query:       qdrant.NewQueryFusion(qdrant.Fusion_RRF),
			Limit:       qdrant.PtrOf(limit),
			WithPayload: qdrant.NewWithPayload(true),
		})
	} else {
		// Dense-only fallback.
		denseName := ragDenseName
		points, err = r.qdrant.Query(qctx, &qdrant.QueryPoints{
			CollectionName: r.collection,
			Query:          qdrant.NewQueryDense(dense),
			Using:          &denseName,
			Limit:          qdrant.PtrOf(limit),
			Filter:         filter,
			WithPayload:    qdrant.NewWithPayload(true),
		})
	}
	if err != nil {
		return ragSearchResponse{}, fmt.Errorf("qdrant query: %w", err)
	}

	// 3. Optional rerank.
	reranked := false
	if useRerank && len(points) > 1 {
		passages := make([]string, len(points))
		for i, p := range points {
			passages[i] = payloadString(p.Payload, "chunk_text")
		}
		rctx, rcancel := context.WithTimeout(ctx, ragEmbedTimeout)
		defer rcancel()
		scores, err := r.embed.rerank(rctx, req.Query, passages)
		if err != nil {
			// Don't fail the whole query: log and fall back to fusion scores.
			slog.Warn("rag: rerank failed, falling back to fusion scores", "err", err)
		} else if len(scores) == len(points) {
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
			ranked := make([]*qdrant.ScoredPoint, 0, req.TopK)
			rerankScores := make([]float32, 0, req.TopK)
			for _, r := range ordered {
				if len(ranked) >= req.TopK {
					break
				}
				ranked = append(ranked, points[r.idx])
				rerankScores = append(rerankScores, r.score)
			}
			// Apply rerank score back so the response carries the more
			// meaningful number.
			for i, p := range ranked {
				p.Score = rerankScores[i]
			}
			points = ranked
			reranked = true
		}
	}

	if len(points) > req.TopK {
		points = points[:req.TopK]
	}

	results := make([]ragSearchHit, 0, len(points))
	for _, p := range points {
		results = append(results, payloadToHit(p))
	}
	return ragSearchResponse{
		Query:    req.Query,
		TookS:    time.Since(t0).Seconds(),
		Reranked: reranked,
		Results:  results,
	}, nil
}

func buildRagFilter(m map[string]string) *qdrant.Filter {
	if len(m) == 0 {
		return nil
	}
	conds := make([]*qdrant.Condition, 0, len(m))
	for k, v := range m {
		conds = append(conds, qdrant.NewMatchKeyword(k, v))
	}
	return &qdrant.Filter{Must: conds}
}

func payloadToHit(p *qdrant.ScoredPoint) ragSearchHit {
	pl := p.Payload
	h := ragSearchHit{
		ArxivID:     payloadString(pl, "arxiv_id"),
		Canonical:   payloadString(pl, "canonical"),
		YYMM:        payloadString(pl, "yymm"),
		Version:     int(payloadInt(pl, "version")),
		SectionPath: payloadStringList(pl, "section_path"),
		ChunkIndex:  int(payloadInt(pl, "chunk_index")),
		Snippet:     payloadString(pl, "chunk_text"),
		Score:       p.Score,
		MDObjectKey: payloadString(pl, "md_object_key"),
		CharStart:   int(payloadInt(pl, "char_start")),
		CharEnd:     int(payloadInt(pl, "char_end")),
		ImageRefs:   payloadStringList(pl, "image_refs"),
		Authors:     payloadStringList(pl, "authors"),
		Categories:  payloadStringList(pl, "categories"),
	}
	if t := payloadString(pl, "title"); t != "" {
		h.Title = &t
	}
	if h.ImageRefs == nil {
		h.ImageRefs = []string{}
	}
	return h
}

func payloadString(m map[string]*qdrant.Value, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.Kind.(*qdrant.Value_StringValue); ok {
		return s.StringValue
	}
	return ""
}

func payloadInt(m map[string]*qdrant.Value, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch k := v.Kind.(type) {
	case *qdrant.Value_IntegerValue:
		return k.IntegerValue
	case *qdrant.Value_DoubleValue:
		return int64(k.DoubleValue)
	}
	return 0
}

func payloadStringList(m map[string]*qdrant.Value, key string) []string {
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

// --- registration ---------------------------------------------------

// RegisterRAG wires /api/rag/search and /api/rag/healthz onto the
// PocketBase router. No-op when the master switch is off or the
// Qdrant/embed endpoints are unset (see package doc).
//
// Disabled in two layers:
//   - PaperAccessEnabled = false  → log + skip
//   - RAGQdrantURL empty or RAGEmbedURL empty → log + skip
//
// The retriever is constructed lazily on the first /api/rag request.
// Startup must not panic / fail just because Qdrant is currently down —
// /api/rag/healthz is what reports degraded health.
func RegisterRAG(se *core.ServeEvent, cfg *config.Config, enforcer *casbin.Enforcer) {
	if !cfg.PaperAccessEnabled {
		slog.Info("rag: disabled (QATLAS_PAPER_ACCESS_ENABLED is off)")
		return
	}
	if strings.TrimSpace(cfg.RAGQdrantURL) == "" || strings.TrimSpace(cfg.RAGEmbedURL) == "" {
		slog.Info("rag: disabled (QATLAS_RAG_QDRANT_URL and QATLAS_RAG_EMBED_URL must both be set)")
		return
	}

	var (
		once    sync.Once
		retr    *ragRetriever
		initErr error
	)
	getRetriever := func() (*ragRetriever, error) {
		once.Do(func() {
			retr, initErr = newRagRetriever(cfg)
			if initErr != nil {
				slog.Error("rag: retriever init failed", "err", initErr)
			}
		})
		return retr, initErr
	}

	slog.Info("rag: enabled",
		"qdrant", cfg.RAGQdrantURL,
		"collection", cfg.RAGQdrantCollection,
		"embed", cfg.RAGEmbedURL)

	se.Router.POST("/api/rag/search",
		scopeGuard(enforcer, "papers", "read", func(e *core.RequestEvent) error {
			r, err := getRetriever()
			if err != nil {
				return router.NewApiError(http.StatusBadGateway, "rag retriever unavailable: "+err.Error(), nil)
			}
			var req ragSearchRequest
			if err := e.BindBody(&req); err != nil {
				return router.NewBadRequestError("invalid JSON: "+err.Error(), nil)
			}
			req.Query = strings.TrimSpace(req.Query)
			if req.Query == "" {
				return router.NewBadRequestError("query must not be empty", nil)
			}
			if len(req.Query) > 2048 {
				return router.NewBadRequestError("query too long (max 2048 chars)", nil)
			}
			resp, err := r.search(e.Request.Context(), req)
			if err != nil {
				slog.Error("rag: search failed", "err", err)
				return router.NewApiError(http.StatusBadGateway, "rag upstream failure: "+err.Error(), nil)
			}
			return e.JSON(http.StatusOK, resp)
		}),
	)

	se.Router.GET("/api/rag/healthz", func(e *core.RequestEvent) error {
		r, err := getRetriever()
		if err != nil {
			return e.JSON(http.StatusOK, ragHealthResponse{Status: "down"})
		}
		return e.JSON(http.StatusOK, r.health(e.Request.Context()))
	})
}
