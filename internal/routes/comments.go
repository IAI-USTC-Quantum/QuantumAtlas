package routes

// comments.go — Q2 block-comment API (plan §12.2):
//
//	GET   /api/papers/{paper_id}/discussions
//	POST  /api/papers/{paper_id}/discussions            (Idempotency-Key)
//	GET   /api/discussions/{discussion_id}              (replies page + ETag)
//	POST  /api/discussions/{discussion_id}/replies      (Idempotency-Key)
//	PATCH /api/discussions/{discussion_id}/status       (root author | admin)
//	PATCH /api/discussions/{discussion_id}/body         (author, If-Match CAS)
//	PATCH /api/discussions/{discussion_id}/replies/{reply_id}/body (author, CAS)
//	GET   /api/discussions/{discussion_id}/revisions    (body + status history)
//
// Guards:
//
//   - commentReadGuard: any authenticated caller holding papers:read OR
//     comments:read (plan §12.2 "读同上" — the Q1 paper-read scope —
//     plus the comments read scope so a comments-only PAT works).
//     System PATs may READ comments.
//   - commentWriteGuard: authenticated + NOT the system PAT (plan
//     §12.1.3: 系统 PAT 对评论一律只读, writes 403) + comments:write
//     scope. Sessions pass via ScopeMaster.
//
// NOTE on admin: the status-change permission consults the users-record
// role flags (is_admin / is_superadmin) directly on the caller's own
// credential. It deliberately does NOT reuse adminGuard — adminGuard
// layers sessionGuard semantics which reject ALL PATs (both user and
// system). Here a USER PAT belonging to an admin must be allowed.
//
// Actor & time are always taken server-side: created_by/actor/editor
// come from re.Auth.Id, timestamps from the store. The client-supplied
// `model` string is stored as a pure declaration (plan §6.3).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/comments"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

const (
	commentsDefaultPerPage = 20
	commentsMaxPerPage     = 100
)

// commentDeps bundles the per-process collaborators of the comment
// routes (wired once in cmd/qatlasd/main.go).
type commentDeps struct {
	cfg     *config.Config
	store   comments.Store
	idem    *comments.Idempotency
	anchors comments.AnchorValidator
}

// RegisterComments wires the Q2 comment surface. store may be a PG
// store (production) — a nil-pool PG store degrades every handler to
// 503 like the papers catalog. anchors may be nil → permissive
// (fixture-first Q2; Q1→Q2 integration swaps in a registry-backed
// validator, plan §8 Q2 / §12.5).
func RegisterComments(
	se *core.ServeEvent,
	cfg *config.Config,
	store comments.Store,
	idem *comments.Idempotency,
	anchors comments.AnchorValidator,
	enforcer *casbin.Enforcer,
) {
	deps := &commentDeps{cfg: cfg, store: store, idem: idem, anchors: anchors}
	if deps.anchors == nil {
		deps.anchors = comments.PermissiveAnchor()
	}

	// Paper-scoped discussion list/create. These specific patterns take
	// precedence over the /api/papers/{path...} catch-alls registered by
	// RegisterPapers (Go 1.22 ServeMux: the more specific pattern wins).
	se.Router.GET("/api/papers/{paper_id}/discussions", commentReadGuard(enforcer, deps.listDiscussions))
	se.Router.POST("/api/papers/{paper_id}/discussions", commentWriteGuard(enforcer, deps.createDiscussion))

	// Discussion-scoped surface.
	se.Router.GET("/api/discussions/{discussion_id}", commentReadGuard(enforcer, deps.getDiscussion))
	se.Router.POST("/api/discussions/{discussion_id}/replies", commentWriteGuard(enforcer, deps.createReply))
	se.Router.PATCH("/api/discussions/{discussion_id}/status", commentWriteGuard(enforcer, deps.setStatus))
	se.Router.PATCH("/api/discussions/{discussion_id}/body", commentWriteGuard(enforcer, deps.editDiscussionBody))
	se.Router.PATCH("/api/discussions/{discussion_id}/replies/{reply_id}/body", commentWriteGuard(enforcer, deps.editReplyBody))
	se.Router.GET("/api/discussions/{discussion_id}/revisions", commentReadGuard(enforcer, deps.revisions))
}

// --- guards -------------------------------------------------------------------

// commentReadGuard: authenticated + (papers:read OR comments:read).
func commentReadGuard(enforcer *casbin.Enforcer, handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return authGuard(func(re *core.RequestEvent) error {
		held, _ := re.Get(authScopesKey).([]string)
		okPaper, err := pat.Allows(enforcer, held, "papers", "read")
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "scope check failed: " + err.Error()})
		}
		okComments, err := pat.Allows(enforcer, held, "comments", "read")
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "scope check failed: " + err.Error()})
		}
		if !okPaper && !okComments {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "insufficient scope: this token lacks papers:read or comments:read (mint a new PAT at /pat with the required scope)",
			})
		}
		return handler(re)
	})
}

// commentWriteGuard: authenticated + not the system PAT (403) +
// comments:write. User PATs and sessions are both fine; the actor is
// the users record mounted on re.Auth.
func commentWriteGuard(enforcer *casbin.Enforcer, handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return authGuard(func(re *core.RequestEvent) error {
		if source, _ := re.Get(authSourceKey).(string); source == authSourceSystemPAT {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "system PATs are read-only for comments: write with a user PAT or browser session (the comment must be attributable to a real account)",
			})
		}
		if re.Auth == nil {
			// Defensive: every non-system auth path mounts re.Auth.
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "comments: write requires an authenticated user identity",
			})
		}
		held, _ := re.Get(authScopesKey).([]string)
		ok, err := pat.Allows(enforcer, held, "comments", "write")
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "scope check failed: " + err.Error()})
		}
		if !ok {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "insufficient scope: this token lacks comments:write (mint a new PAT at /pat with the required scope)",
				"obj":    "comments",
				"act":    "write",
			})
		}
		return handler(re)
	})
}

// commentActorID returns the authenticated users-record id.
func commentActorID(re *core.RequestEvent) string {
	if re.Auth == nil {
		return ""
	}
	return re.Auth.Id
}

// commentCallerIsAdmin reports the users-record role flags (NOT
// adminGuard semantics — user PATs are legitimate here; see the file
// comment).
func commentCallerIsAdmin(re *core.RequestEvent) bool {
	if re.Auth == nil {
		return false
	}
	return re.Auth.GetBool(auth.IsAdminField) || re.Auth.GetBool(auth.IsSuperadminField)
}

// --- JSON DTOs ----------------------------------------------------------------

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func discussionJSON(d comments.Discussion) map[string]any {
	return map[string]any{
		"discussion_id": d.DiscussionID,
		"paper_id":      d.PaperID,
		"anchor": map[string]any{
			"parse_revision": d.ParseRevision,
			"page_idx":       d.PageIdx,
			"block_index":    d.BlockIndex,
		},
		"type":        d.Type,
		"scope":       d.Scope,
		"status":      nullIfEmpty(d.Status),
		"body":        d.Body,
		"created_by":  d.CreatedBy,
		"model":       nullIfEmpty(d.Model),
		"revision":    d.Revision,
		"reply_count": d.ReplyCount,
		"created_at":  d.CreatedAt.UTC().Format(timeFormatRFC3339),
		"updated_at":  d.UpdatedAt.UTC().Format(timeFormatRFC3339),
	}
}

func replyJSON(r comments.Reply) map[string]any {
	return map[string]any{
		"reply_id":      r.ReplyID,
		"discussion_id": r.DiscussionID,
		"body":          r.Body,
		"created_by":    r.CreatedBy,
		"model":         nullIfEmpty(r.Model),
		"revision":      r.Revision,
		"created_at":    r.CreatedAt.UTC().Format(timeFormatRFC3339),
		"updated_at":    r.UpdatedAt.UTC().Format(timeFormatRFC3339),
	}
}

// --- handlers -------------------------------------------------------------------

// listDiscussions: GET /api/papers/{paper_id}/discussions
func (d *commentDeps) listDiscussions(re *core.RequestEvent) error {
	paperID := re.Request.PathValue("paper_id")
	if !strings.HasPrefix(paperID, "qa_") || len(paperID) <= len("qa_") {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid paper_id (want the qa_ surrogate id): " + paperID,
		})
	}
	q := re.Request.URL.Query()
	f := comments.ListFilter{PaperID: paperID}

	switch v := q.Get("scope"); v {
	case "":
	case comments.ScopePublic:
		f.Scopes = []string{comments.ScopePublic}
	case comments.ScopeLean:
		// Lean view includes shared public discussion (plan §12.1.5).
		f.Scopes = []string{comments.ScopePublic, comments.ScopeLean}
	default:
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid scope (want public/lean): " + v,
		})
	}
	if v := q.Get("type"); v != "" {
		if !comments.ValidType(v) {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid type (want [a-z0-9_:-]{1,64}): " + v,
			})
		}
		f.Type = v
	}
	switch v := q.Get("status"); v {
	case "":
	case "none":
		f.StatusIsNull = true
	case comments.StatusPending, comments.StatusConfirmed, comments.StatusRetracted:
		s := v
		f.Status = &s
	default:
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid status (want pending/confirmed/retracted/none): " + v,
		})
	}
	if v := q.Get("parse_revision"); v != "" {
		f.ParseRevision = v
	}
	if v := q.Get("page_idx"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid page_idx (want >=0): " + v})
		}
		f.PageIdx = &n
	}
	if v := q.Get("block_index"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid block_index (want >=1): " + v})
		}
		f.BlockIndex = &n
	}
	f.Cursor = q.Get("cursor")
	f.Limit = commentsDefaultPerPage
	if v := q.Get("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid per_page: " + v})
		}
		if n > commentsMaxPerPage {
			n = commentsMaxPerPage
		}
		f.Limit = n
	}

	items, err := d.store.ListDiscussions(re.Request.Context(), f)
	if err != nil {
		return commentStoreError(re, "list discussions", err)
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, discussionJSON(it))
	}
	resp := map[string]any{"items": out, "next_cursor": nil}
	if len(items) == f.Limit && f.Limit > 0 {
		resp["next_cursor"] = items[len(items)-1].DiscussionID
	}
	return re.JSON(http.StatusOK, resp)
}

// createDiscussionBody is the POST /discussions payload. There is no
// author field on purpose (plan §6.3: 不接收 body 自报 author).
type createDiscussionBody struct {
	ParseRevision string `json:"parse_revision"`
	PageIdx       *int   `json:"page_idx"`
	BlockIndex    *int   `json:"block_index"`
	Type          string `json:"type"`
	Scope         string `json:"scope"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Body          string `json:"body"`
	Model         string `json:"model"`
}

// createDiscussion: POST /api/papers/{paper_id}/discussions
func (d *commentDeps) createDiscussion(re *core.RequestEvent) error {
	paperID := re.Request.PathValue("paper_id")
	if !strings.HasPrefix(paperID, "qa_") || len(paperID) <= len("qa_") {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid paper_id (want the qa_ surrogate id): " + paperID,
		})
	}
	raw, err := d.readBody(re)
	if err != nil {
		return err
	}

	// Idempotency replay / conflict (plan §12.2).
	key := re.Request.Header.Get("Idempotency-Key")
	reqHash := comments.RequestHash(http.MethodPost, re.Request.URL.Path, raw)
	if key != "" && d.idem != nil {
		if status, body, ok, ierr := d.idem.Result(key, reqHash); ierr != nil {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": "Idempotency-Key was already used with a different request body",
			})
		} else if ok {
			re.Response.Header().Set("Content-Type", "application/json")
			re.Response.Header().Set("Idempotency-Replayed", "true")
			return re.JSON(status, json.RawMessage(body))
		}
	}

	var in createDiscussionBody
	if err := json.Unmarshal(raw, &in); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	if in.ParseRevision == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse_revision is required"})
	}
	if in.PageIdx == nil || *in.PageIdx < 0 {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "page_idx is required (>= 0)"})
	}
	if in.BlockIndex == nil || *in.BlockIndex < 1 {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "block_index is required (>= 1)"})
	}
	if in.Type == "" {
		in.Type = comments.TypeNormal
	}
	if !comments.ValidType(in.Type) {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid type (want [a-z0-9_:-]{1,64}): " + in.Type,
		})
	}
	if in.Scope == "" {
		in.Scope = comments.ScopePublic
	}
	if !comments.ValidScope(in.Scope) {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid scope (want public/lean): " + in.Scope})
	}
	if !comments.ValidStatus(in.Status) {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid status (want pending/confirmed/retracted or omitted): " + in.Status,
		})
	}
	if in.Status != "" && in.Reason == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "setting an initial status requires a reason",
		})
	}
	if cerr := d.checkBodyLimit(re, in.Body); cerr != nil {
		return cerr
	}
	if in.Body == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "body is required"})
	}
	if err := d.anchors.ValidateBlock(paperID, in.ParseRevision, *in.PageIdx, *in.BlockIndex); err != nil {
		if errors.Is(err, comments.ErrAnchorNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "anchor block not found in this parse revision (no nearest-block fallback)",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}

	disc, err := d.store.CreateDiscussion(re.Request.Context(), comments.CreateDiscussionInput{
		PaperID:       paperID,
		ParseRevision: in.ParseRevision,
		PageIdx:       *in.PageIdx,
		BlockIndex:    *in.BlockIndex,
		Type:          in.Type,
		Scope:         in.Scope,
		Status:        in.Status,
		InitialReason: in.Reason,
		Body:          in.Body,
		CreatedBy:     commentActorID(re),
		Model:         in.Model,
	})
	if err != nil {
		return commentStoreError(re, "create discussion", err)
	}
	resp, _ := json.Marshal(discussionJSON(disc))
	if key != "" && d.idem != nil {
		d.idem.Remember(key, reqHash, http.StatusCreated, resp)
	}
	re.Response.Header().Set("Location", "/api/discussions/"+disc.DiscussionID)
	return re.JSON(http.StatusCreated, json.RawMessage(resp))
}

// getDiscussion: GET /api/discussions/{discussion_id} — detail plus the
// first (or cursor-continued) replies page.
func (d *commentDeps) getDiscussion(re *core.RequestEvent) error {
	id := re.Request.PathValue("discussion_id")
	disc, found, err := d.store.GetDiscussion(re.Request.Context(), id)
	if err != nil {
		return commentStoreError(re, "get discussion", err)
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "discussion not found: " + id})
	}
	q := re.Request.URL.Query()
	limit := commentsDefaultPerPage
	if v := q.Get("per_page"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid per_page: " + v})
		}
		if n > commentsMaxPerPage {
			n = commentsMaxPerPage
		}
		limit = n
	}
	replies, err := d.store.ListReplies(re.Request.Context(), id, q.Get("cursor"), limit)
	if err != nil {
		return commentStoreError(re, "list replies", err)
	}
	out := make([]map[string]any, 0, len(replies))
	for _, r := range replies {
		out = append(out, replyJSON(r))
	}
	resp := discussionJSON(disc)
	resp["replies"] = out
	resp["replies_next_cursor"] = nil
	if len(replies) == limit && limit > 0 {
		resp["replies_next_cursor"] = replies[len(replies)-1].ReplyID
	}
	// ETag = body revision integer (plan §12.2 通用合同).
	re.Response.Header().Set("ETag", strconv.Itoa(disc.Revision))
	return re.JSON(http.StatusOK, resp)
}

// createReplyBody is the POST /replies payload.
type createReplyBody struct {
	Body  string `json:"body"`
	Model string `json:"model"`
}

// createReply: POST /api/discussions/{discussion_id}/replies
func (d *commentDeps) createReply(re *core.RequestEvent) error {
	id := re.Request.PathValue("discussion_id")
	raw, err := d.readBody(re)
	if err != nil {
		return err
	}
	key := re.Request.Header.Get("Idempotency-Key")
	reqHash := comments.RequestHash(http.MethodPost, re.Request.URL.Path, raw)
	if key != "" && d.idem != nil {
		if status, body, ok, ierr := d.idem.Result(key, reqHash); ierr != nil {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": "Idempotency-Key was already used with a different request body",
			})
		} else if ok {
			re.Response.Header().Set("Content-Type", "application/json")
			re.Response.Header().Set("Idempotency-Replayed", "true")
			return re.JSON(status, json.RawMessage(body))
		}
	}

	var in createReplyBody
	if err := json.Unmarshal(raw, &in); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	if in.Body == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "body is required"})
	}
	if cerr := d.checkBodyLimit(re, in.Body); cerr != nil {
		return cerr
	}

	reply, err := d.store.CreateReply(re.Request.Context(), comments.CreateReplyInput{
		DiscussionID: id,
		Body:         in.Body,
		CreatedBy:    commentActorID(re),
		Model:        in.Model,
	})
	if err != nil {
		return commentStoreError(re, "create reply", err)
	}
	resp, _ := json.Marshal(replyJSON(reply))
	if key != "" && d.idem != nil {
		d.idem.Remember(key, reqHash, http.StatusCreated, resp)
	}
	return re.JSON(http.StatusCreated, json.RawMessage(resp))
}

// setStatusBody is the PATCH /status payload.
type setStatusBody struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// setStatus: PATCH /api/discussions/{discussion_id}/status — root
// author or platform admin (plan §12.1.1); reason mandatory; every
// change leaves a trail; reopen allowed (§12.1.4).
func (d *commentDeps) setStatus(re *core.RequestEvent) error {
	id := re.Request.PathValue("discussion_id")
	raw, err := d.readBody(re)
	if err != nil {
		return err
	}
	var in setStatusBody
	if err := json.Unmarshal(raw, &in); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	if !comments.ValidStatus(in.Status) || in.Status == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid status (want pending/confirmed/retracted): " + in.Status,
		})
	}
	if strings.TrimSpace(in.Reason) == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "reason is required for status changes"})
	}

	disc, found, err := d.store.GetDiscussion(re.Request.Context(), id)
	if err != nil {
		return commentStoreError(re, "get discussion", err)
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "discussion not found: " + id})
	}
	actor := commentActorID(re)
	if disc.CreatedBy != actor && !commentCallerIsAdmin(re) {
		return re.JSON(http.StatusForbidden, map[string]string{
			"detail": "only the discussion author or a platform admin may change the status",
		})
	}

	updated, err := d.store.SetStatus(re.Request.Context(), id, in.Status, in.Reason, actor)
	if err != nil {
		return commentStoreError(re, "set status", err)
	}
	return re.JSON(http.StatusOK, discussionJSON(updated))
}

// editBodyPayload is the PATCH .../body payload.
type editBodyPayload struct {
	Body string `json:"body"`
}

// editDiscussionBody: PATCH /api/discussions/{discussion_id}/body —
// author-only (even admins edit nobody else's words, §12.1.2),
// If-Match CAS on the revision integer.
func (d *commentDeps) editDiscussionBody(re *core.RequestEvent) error {
	id := re.Request.PathValue("discussion_id")
	expected, ok := parseIfMatchRevision(re)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "If-Match header with the current revision integer is required (GET the discussion and use its ETag)",
		})
	}
	raw, err := d.readBody(re)
	if err != nil {
		return err
	}
	var in editBodyPayload
	if err := json.Unmarshal(raw, &in); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	if in.Body == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "body is required"})
	}
	if cerr := d.checkBodyLimit(re, in.Body); cerr != nil {
		return cerr
	}

	disc, found, err := d.store.GetDiscussion(re.Request.Context(), id)
	if err != nil {
		return commentStoreError(re, "get discussion", err)
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "discussion not found: " + id})
	}
	if disc.CreatedBy != commentActorID(re) {
		return re.JSON(http.StatusForbidden, map[string]string{
			"detail": "only the author may edit this body",
		})
	}

	updated, err := d.store.UpdateDiscussionBody(re.Request.Context(), id, expected, in.Body, commentActorID(re))
	if err != nil {
		return commentStoreError(re, "edit discussion body", err)
	}
	re.Response.Header().Set("ETag", strconv.Itoa(updated.Revision))
	return re.JSON(http.StatusOK, discussionJSON(updated))
}

// editReplyBody: PATCH /api/discussions/{discussion_id}/replies/{reply_id}/body.
func (d *commentDeps) editReplyBody(re *core.RequestEvent) error {
	discussionID := re.Request.PathValue("discussion_id")
	replyID := re.Request.PathValue("reply_id")
	expected, ok := parseIfMatchRevision(re)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "If-Match header with the current revision integer is required (use the reply's revision)",
		})
	}
	raw, err := d.readBody(re)
	if err != nil {
		return err
	}
	var in editBodyPayload
	if err := json.Unmarshal(raw, &in); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	if in.Body == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "body is required"})
	}
	if cerr := d.checkBodyLimit(re, in.Body); cerr != nil {
		return cerr
	}

	reply, found, err := d.store.GetReply(re.Request.Context(), replyID)
	if err != nil {
		return commentStoreError(re, "get reply", err)
	}
	if !found || reply.DiscussionID != discussionID {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "reply not found: " + replyID})
	}
	if reply.CreatedBy != commentActorID(re) {
		return re.JSON(http.StatusForbidden, map[string]string{
			"detail": "only the author may edit this body",
		})
	}

	updated, err := d.store.UpdateReplyBody(re.Request.Context(), replyID, expected, in.Body, commentActorID(re))
	if err != nil {
		return commentStoreError(re, "edit reply body", err)
	}
	re.Response.Header().Set("ETag", strconv.Itoa(updated.Revision))
	return re.JSON(http.StatusOK, replyJSON(updated))
}

// revisions: GET /api/discussions/{discussion_id}/revisions — body
// revisions (discussion + its replies) and status events, both oldest
// first (plan §12.2 "修订与状态历史").
func (d *commentDeps) revisions(re *core.RequestEvent) error {
	id := re.Request.PathValue("discussion_id")
	bodyRevs, events, err := d.store.History(re.Request.Context(), id)
	if err != nil {
		return commentStoreError(re, "history", err)
	}
	revs := make([]map[string]any, 0, len(bodyRevs))
	for _, r := range bodyRevs {
		revs = append(revs, map[string]any{
			"target":     r.Target,
			"target_id":  r.TargetID,
			"old_body":   r.OldBody,
			"new_body":   r.NewBody,
			"editor":     r.Editor,
			"created_at": r.At.UTC().Format(timeFormatRFC3339),
		})
	}
	evs := make([]map[string]any, 0, len(events))
	for _, e := range events {
		evs = append(evs, map[string]any{
			"from":       nullIfEmpty(e.From),
			"to":         e.To,
			"reason":     e.Reason,
			"actor":      e.Actor,
			"created_at": e.At.UTC().Format(timeFormatRFC3339),
		})
	}
	return re.JSON(http.StatusOK, map[string]any{
		"body_revisions": revs,
		"status_events":  evs,
	})
}

// --- helpers ---------------------------------------------------------------------

// timeFormatRFC3339 names the wire timestamp format used by the comment
// DTOs (RFC3339, UTC).
const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

// readBody slurps the (bounded) request body. Oversize → 413 (never
// truncated, plan §9). Malformed reads surface as 400.
func (d *commentDeps) readBody(re *core.RequestEvent) ([]byte, error) {
	max := int64(1 << 20)
	if d.cfg != nil && d.cfg.CommentsMaxRequestBytes > 0 {
		max = int64(d.cfg.CommentsMaxRequestBytes)
	}
	re.Request.Body = http.MaxBytesReader(re.Response, re.Request.Body, max)
	raw, err := io.ReadAll(re.Request.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, re.JSON(http.StatusRequestEntityTooLarge, map[string]string{
				"detail": "request body exceeds the comment request limit; split the content instead of truncating",
			})
		}
		return nil, re.JSON(http.StatusBadRequest, map[string]string{"detail": "read request body: " + err.Error()})
	}
	return raw, nil
}

// checkBodyLimit enforces the per-body Unicode character budget
// (default 20000, config comments.max_body_chars). Oversize → 413
// with a split hint, never a silent truncate (plan §12.2 通用合同).
func (d *commentDeps) checkBodyLimit(re *core.RequestEvent, body string) error {
	max := 20000
	if d.cfg != nil && d.cfg.CommentsMaxBodyChars > 0 {
		max = d.cfg.CommentsMaxBodyChars
	}
	if n := utf8.RuneCountInString(body); n > max {
		return re.JSON(http.StatusRequestEntityTooLarge, map[string]string{
			"detail": "body exceeds the maximum of " + strconv.Itoa(max) +
				" Unicode characters (" + strconv.Itoa(n) + " given); split the comment instead of truncating",
		})
	}
	return nil
}

// parseIfMatchRevision extracts the CAS revision from If-Match,
// tolerating quoted ETag forms ("5", W/"5") and a bare integer.
func parseIfMatchRevision(re *core.RequestEvent) (int, bool) {
	v := strings.TrimSpace(re.Request.Header.Get("If-Match"))
	if v == "" {
		return 0, false
	}
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// commentStoreError maps domain errors onto the shared error contract.
func commentStoreError(re *core.RequestEvent, op string, err error) error {
	switch {
	case errors.Is(err, comments.ErrNotFound):
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
	case errors.Is(err, comments.ErrRevisionConflict):
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": "revision conflict: the body changed since you read it (re-fetch and retry with the new ETag)",
		})
	case errors.Is(err, comments.ErrEmptyReason):
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "reason is required for status changes"})
	case errors.Is(err, comments.ErrUnavailable):
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "comment store unavailable (PostgreSQL unreachable); retry shortly",
		})
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{"detail": op + ": " + err.Error()})
}
