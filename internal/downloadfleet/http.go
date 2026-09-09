package downloadfleet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

// WorkerHandler is self-authenticating and MUST NOT share admin/session bearer
// authentication. Parent mounts these exact paths or the BasePath wildcard.
func (s *Service) WorkerHandler() http.Handler { return http.HandlerFunc(s.serveWorker) }
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	// PocketBase's request wrapper rewinds at EOF. Decode from a bounded
	// independent buffer so a trailing-value check cannot parse a replay.
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}
func httpError(w http.ResponseWriter, e error) {
	status := http.StatusServiceUnavailable
	message := "worker service temporarily unavailable"
	switch {
	case errors.Is(e, ErrDisabled):
		status = 503
		message = e.Error()
	case errors.Is(e, ErrUnauthorized):
		status = 401
		message = e.Error()
	case errors.Is(e, ErrForbidden):
		status = 403
		message = e.Error()
	case errors.Is(e, ErrNotFound):
		status = 404
		message = e.Error()
	case errors.Is(e, ErrConflict):
		status = 409
		message = e.Error()
	case errors.Is(e, context.DeadlineExceeded):
		status = 408
		message = "request deadline exceeded"
	default:
		slog.Warn("download fleet request failed", "error", e)
	}
	jsonResponse(w, status, wp.ErrorResponse{Error: message})
}
func (s *Service) serveWorker(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		httpError(w, ErrDisabled)
		return
	}
	path := r.URL.Path
	method := http.MethodPost
	if path == wp.StatusPath || strings.HasPrefix(path, wp.ReceiptPath) {
		method = http.MethodGet
	} else if strings.HasPrefix(path, wp.UploadPath) {
		method = http.MethodPut
	}
	known := path == wp.RegisterPath || path == wp.StatusPath || path == wp.HeartbeatPath || path == wp.ClaimPath || path == wp.ReportPath || strings.HasPrefix(path, wp.UploadPath) || strings.HasPrefix(path, wp.ReceiptPath)
	if !known {
		http.NotFound(w, r)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		jsonResponse(w, 405, wp.ErrorResponse{Error: "method not allowed"})
		return
	}
	budget := s.cfg.LeaseDuration
	if strings.HasPrefix(path, wp.UploadPath) {
		budget = s.cfg.UploadTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	bad := func() { jsonResponse(w, 400, wp.ErrorResponse{Error: "invalid request"}) }
	if path == wp.RegisterPath {
		var req wp.RegisterRequest
		if decodeJSON(w, r, &req) != nil {
			bad()
			return
		}
		if !validID.MatchString(req.ID) || len(req.Secret) < 32 || len(req.Secret) > 256 || len(req.EnrollmentToken) > 256 || len(req.Name) > 200 {
			bad()
			return
		}
		n, e := s.register(ctx, req)
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, n)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		httpError(w, ErrUnauthorized)
		return
	}
	n, e := s.authenticate(ctx, strings.TrimPrefix(auth, "Bearer "))
	if e != nil {
		httpError(w, e)
		return
	}
	if path == wp.StatusPath {
		jsonResponse(w, 200, n)
		return
	}
	if n.Status != "approved" && n.Status != "draining" {
		httpError(w, ErrForbidden)
		return
	}
	var result any
	switch {
	case path == wp.HeartbeatPath:
		var req wp.HeartbeatRequest
		if decodeJSON(w, r, &req) != nil {
			bad()
			return
		}
		if req.Capacity < 0 || req.DiskFreeBytes < 0 || req.SpoolBytes < 0 || len(req.RunningAttemptIDs) > 128 {
			bad()
			return
		}
		result, e = s.heartbeat(ctx, n, req)
	case path == wp.ClaimPath:
		var req wp.ClaimRequest
		if decodeJSON(w, r, &req) != nil {
			bad()
			return
		}
		result, e = s.claim(ctx, n, req)
	case path == wp.ReportPath:
		var req wp.ReportRequest
		if decodeJSON(w, r, &req) != nil {
			bad()
			return
		}
		if !validFailure(req.Failure) || len(req.Trace) > 64 {
			bad()
			return
		}
		result, e = s.report(ctx, n, req)
	case strings.HasPrefix(path, wp.ReceiptPath):
		id := strings.TrimPrefix(path, wp.ReceiptPath)
		if !validID.MatchString(id) {
			httpError(w, ErrNotFound)
			return
		}
		result, e = s.receipt(ctx, n.ID, id)
	case strings.HasPrefix(path, wp.UploadPath):
		id := strings.TrimPrefix(path, wp.UploadPath)
		size, err := strconv.ParseInt(r.Header.Get(wp.HeaderSize), 10, 64)
		if err != nil || size < 1 || size > s.cfg.MaxPDFBytes || (r.ContentLength >= 0 && r.ContentLength != size) {
			bad()
			return
		}
		var meta wp.ResultMetadata
		encoded := r.Header.Get(wp.HeaderResultMetadata)
		if len(encoded) > 16<<10 {
			bad()
			return
		}
		if encoded != "" {
			raw, err := base64.RawURLEncoding.DecodeString(encoded)
			if err != nil || json.Unmarshal(raw, &meta) != nil || len(meta.Trace) > 64 {
				bad()
				return
			}
		}
		// SetReadDeadline bounds slow streaming even when a reader ignores Context.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.UploadTimeout))
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxPDFBytes+1)
		result, e = s.upload(ctx, n, id, r.Header.Get(wp.HeaderSHA256), size, r.Header.Get(wp.HeaderSourceURL), r.Header.Get(wp.HeaderStrategy), meta, r.Body)
	}
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, result)
}
