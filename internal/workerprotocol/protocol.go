// Package workerprotocol defines the versioned outbound-worker wire contract.
// Workers initiate every request. All routes except register require
// Authorization: Bearer <persistent worker secret>. Never send enrollment tokens
// after registration. Pending workers may only call GET status.
package workerprotocol

import "time"

const BasePath = "/api/downloader/workers/v2"
const (
	RegisterPath         = BasePath + "/register"  // POST RegisterRequest -> Node
	StatusPath           = BasePath + "/status"    // GET -> Node
	HeartbeatPath        = BasePath + "/heartbeat" // POST HeartbeatRequest -> HeartbeatResponse
	ClaimPath            = BasePath + "/claim"     // POST ClaimRequest -> ClaimResponse (empty attempts is normal)
	ReportPath           = BasePath + "/report"    // POST ReportRequest -> Receipt
	UploadPath           = BasePath + "/upload/"   // PUT {attempt_id}, application/pdf; headers below -> Receipt
	ReceiptPath          = BasePath + "/receipts/" // GET {attempt_id} -> Receipt; never destructive
	HeaderSHA256         = "X-PDF-SHA256"
	HeaderSize           = "X-PDF-Size"
	HeaderSourceURL      = "X-Source-URL"
	HeaderStrategy       = "X-Download-Strategy"
	HeaderResultMetadata = "X-Download-Result" // base64.RawURLEncoding JSON ResultMetadata, <=16KiB header
)

type RegisterRequest struct {
	ID              string `json:"id"` // client-generated random stable ID, 16..128 ASCII [A-Za-z0-9_-]
	Name            string `json:"name"`
	EnrollmentToken string `json:"enrollment_token"`
	Secret          string `json:"secret"` // client-generated cryptographically random, >=32 chars; persist BEFORE register
}
type Node struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"` // pending, approved, draining, rejected, revoked
	LastSeen      *time.Time `json:"last_seen"`
	Capacity      int        `json:"capacity"`
	Running       int        `json:"running"`
	BrowserOK     bool       `json:"browser_ok"`
	DiskFreeBytes int64      `json:"disk_free_bytes"`
	SpoolBytes    int64      `json:"spool_bytes"`
	LastError     string     `json:"last_error"`
}
type HeartbeatRequest struct {
	Capacity          int      `json:"capacity"`
	BrowserOK         bool     `json:"browser_ok"`
	DiskFreeBytes     int64    `json:"disk_free_bytes"`
	SpoolBytes        int64    `json:"spool_bytes"`
	LastError         string   `json:"last_error"`
	RunningAttemptIDs []string `json:"running_attempt_ids"`
}
type HeartbeatResponse struct {
	Status       string               `json:"status"`
	LeaseExpires map[string]time.Time `json:"lease_expires"` // absent IDs are fenced; stop them
}
type ClaimRequest struct {
	Limit int `json:"limit"`
}
type PaperRef struct {
	ArxivID    string   `json:"arxiv_id,omitempty"`
	DOI        string   `json:"doi,omitempty"`
	OpenAlexID string   `json:"openalex_id,omitempty"`
	Title      string   `json:"title,omitempty"`
	Authors    []string `json:"authors,omitempty"`
	Year       int      `json:"year,omitempty"`
}
type Assignment struct {
	TaskID       string    `json:"task_id"`
	AttemptID    string    `json:"attempt_id"`
	Ref          PaperRef  `json:"ref"`
	LeaseExpires time.Time `json:"lease_expires"`
	Deadline     time.Time `json:"deadline"`
	MaxPDFBytes  int64     `json:"max_pdf_bytes"`
}
type ClaimResponse struct {
	Attempts []Assignment `json:"attempts"`
}
type Trace struct {
	Strategy string `json:"strategy"`
	URL      string `json:"url,omitempty"`
	Error    string `json:"error,omitempty"`
	Millis   int64  `json:"ms"`
}

// Failures are retryable on another worker except invalid_identifier and cancelled.
const (
	FailureNetwork           = "network"
	FailureTimeout           = "timeout"
	FailureNotFound          = "not_found"
	FailurePaywall           = "paywall"
	FailureChallenge         = "challenge"
	FailureInvalidPDF        = "invalid_pdf"
	FailureDisk              = "disk"
	FailureInternal          = "internal"
	FailureInvalidIdentifier = "invalid_identifier"
	FailureCancelled         = "cancelled"
)

type ReportRequest struct {
	AttemptID string  `json:"attempt_id"`
	Failure   string  `json:"failure"`
	Error     string  `json:"error"`
	Trace     []Trace `json:"trace,omitempty"`
}
type Receipt struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	State     string `json:"state"` // running, staged, done, failed, expired
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Error     string `json:"error,omitempty"`
}
type ResultMetadata struct {
	DOI            string  `json:"doi,omitempty"`
	ArxivCanonical string  `json:"arxiv_canonical,omitempty"`
	ArxivVersion   int     `json:"arxiv_version,omitempty"`
	SourceURL      string  `json:"source_url,omitempty"`
	Strategy       string  `json:"strategy,omitempty"`
	Trace          []Trace `json:"trace,omitempty"`
}
type ErrorResponse struct {
	Error string `json:"error"`
}
