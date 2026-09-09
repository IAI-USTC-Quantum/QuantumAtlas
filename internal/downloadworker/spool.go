package downloadworker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/gofrs/flock"
)

type Identity struct {
	ID         string `json:"id"`
	Secret     string `json:"secret"`
	MasterURL  string `json:"master_url"`
	Registered bool   `json:"registered"`
}

type Record struct {
	Assignment workerprotocol.Assignment     `json:"assignment"`
	State      string                        `json:"state"`
	Created    time.Time                     `json:"created"`
	ReadyAt    time.Time                     `json:"ready_at,omitempty"`
	Size       int64                         `json:"size,omitempty"`
	SHA256     string                        `json:"sha256,omitempty"`
	Strategy   string                        `json:"strategy,omitempty"`
	Metadata   workerprotocol.ResultMetadata `json:"metadata,omitempty"`
	Failure    string                        `json:"failure,omitempty"`
	Error      string                        `json:"error,omitempty"`
}

type Spool struct {
	mu      sync.Mutex
	dir     string
	max     int64
	ttl     time.Duration
	lock    *flock.Flock
	records map[string]Record
	busy    map[string]bool
}

// atomicJSON commits both the file data and its directory entry before returning.
func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicFile(path, strings.NewReader(string(b)))
}
func atomicFile(path string, r io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = io.Copy(f, r)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func OpenSpool(dir string, max int64, ttl time.Duration) (*Spool, error) {
	if max <= 0 || ttl <= 0 {
		return nil, errors.New("invalid spool limits")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "worker.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("worker data directory is already in use")
	}
	s := &Spool{dir: dir, max: max, ttl: ttl, lock: lock, records: map[string]Record{}, busy: map[string]bool{}}
	fail := func(err error) (*Spool, error) { _ = lock.Unlock(); return nil, err }
	b, err := os.ReadFile(filepath.Join(dir, "records.json"))
	if err == nil {
		if err = json.Unmarshal(b, &s.records); err != nil {
			return fail(errors.New("invalid spool catalog; refusing to discard results"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if s.records == nil {
		return fail(errors.New("invalid spool catalog"))
	}
	for id, r := range s.records {
		if id != r.Assignment.AttemptID || !safeID(id) {
			return fail(errors.New("invalid spool attempt identity"))
		}
		switch r.State {
		case "running":
			r.State = "failed"
			r.Failure = workerprotocol.FailureInternal
			r.Error = "worker restarted during download"
			s.records[id] = r
		case "ready":
			f, e := os.Open(s.pdfPath(id))
			if e != nil {
				return fail(errors.New("ready spool PDF is missing"))
			}
			h := sha256.New()
			n, e := io.Copy(h, f)
			_ = f.Close()
			if e != nil || n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
				return fail(errors.New("ready spool PDF failed integrity check"))
			}
		case "failed", "expired":
		default:
			return fail(errors.New("unknown spool state"))
		}
	}
	if err = s.saveLocked(); err != nil {
		return fail(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fail(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") {
			if err = os.Remove(filepath.Join(dir, name)); err != nil {
				return fail(err)
			}
		}
		if strings.HasSuffix(name, ".pdf") {
			r, ok := s.records[strings.TrimSuffix(name, ".pdf")]
			if !ok || r.State != "ready" {
				if err = os.Remove(filepath.Join(dir, name)); err != nil {
					return fail(err)
				}
			}
		}
	}
	if err = syncDir(dir); err != nil {
		return fail(err)
	}
	return s, nil
}
func safeID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *Spool) Close() error             { return s.lock.Unlock() }
func (s *Spool) pdfPath(id string) string { return filepath.Join(s.dir, id+".pdf") }
func (s *Spool) saveLocked() error {
	return atomicJSON(filepath.Join(s.dir, "records.json"), s.records)
}
func (s *Spool) LoadIdentity(master string) (Identity, error) {
	path := filepath.Join(s.dir, "identity.json")
	var id Identity
	b, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(b, &id) != nil || !safeID(id.ID) || len(id.Secret) < 32 || id.MasterURL != master {
			return id, errors.New("worker identity is invalid or belongs to a different master")
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	var random [48]byte
	if _, err = rand.Read(random[:]); err != nil {
		return id, err
	}
	id = Identity{ID: hex.EncodeToString(random[:16]), Secret: hex.EncodeToString(random[16:]), MasterURL: master}
	return id, atomicJSON(path, id)
}
func (s *Spool) SaveIdentity(id Identity) error {
	return atomicJSON(filepath.Join(s.dir, "identity.json"), id)
}
func (s *Spool) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
func (s *Spool) Usage() (used, free int64) { s.mu.Lock(); defer s.mu.Unlock(); return s.usageLocked() }
func (s *Spool) usageLocked() (used, free int64) {
	for _, r := range s.records {
		if r.State == "ready" {
			used += r.Size
		}
	}
	var st syscall.Statfs_t
	if syscall.Statfs(s.dir, &st) == nil {
		free = int64(st.Bavail) * int64(st.Bsize)
	}
	return
}
func (s *Spool) Slots(limit int) int { s.mu.Lock(); defer s.mu.Unlock(); return s.slotsLocked(limit) }
func (s *Spool) slotsLocked(limit int) int {
	used, free := s.usageLocked()
	running := 0
	for _, r := range s.records {
		if r.State == "running" {
			running++
		}
	}
	remaining := s.max - used - int64(running)*MaxPDFBytes
	if f := free - int64(running)*MaxPDFBytes - (1 << 20); f < remaining {
		remaining = f
	}
	slots := int(remaining / MaxPDFBytes)
	if slots < 0 {
		slots = 0
	}
	if available := limit - running; slots > available {
		slots = available
	}
	if slots < 0 {
		return 0
	}
	return slots
}
func (s *Spool) Start(a workerprotocol.Assignment, concurrency int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !safeID(a.AttemptID) || !safeID(a.TaskID) {
		return errors.New("invalid assignment identity")
	}
	if _, ok := s.records[a.AttemptID]; ok {
		return errors.New("assignment already recorded")
	}
	if s.slotsLocked(concurrency) < 1 {
		return errors.New("spool capacity exhausted")
	}
	s.records[a.AttemptID] = Record{Assignment: a, State: "running", Created: now}
	if err := s.saveLocked(); err != nil {
		delete(s.records, a.AttemptID)
		return err
	}
	return nil
}
func (s *Spool) Ready(id string, body io.Reader, metadata workerprotocol.ResultMetadata, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok || r.State != "running" {
		return errors.New("attempt is not running")
	}
	max := MaxPDFBytes
	if r.Assignment.MaxPDFBytes > 0 && r.Assignment.MaxPDFBytes < max {
		max = r.Assignment.MaxPDFBytes
	}
	// A bounded temp write enforces the reservation even if a fetch adapter is faulty.
	h := sha256.New()
	lr := io.LimitReader(body, max+1)
	if err := atomicFile(s.pdfPath(id), io.TeeReader(lr, h)); err != nil {
		return err
	}
	info, err := os.Stat(s.pdfPath(id))
	if err != nil {
		return err
	}
	if info.Size() > max {
		_ = os.Remove(s.pdfPath(id))
		return errors.New("PDF exceeds task size limit")
	}
	old := r
	r.State = "ready"
	r.ReadyAt = now
	r.Size = info.Size()
	r.SHA256 = hex.EncodeToString(h.Sum(nil))
	r.Strategy = metadata.Strategy
	r.Metadata = metadata
	s.records[id] = r
	if err = s.saveLocked(); err != nil {
		s.records[id] = old
		return err
	}
	return nil
}
func (s *Spool) Fail(id, code, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return errors.New("unknown attempt")
	}
	if r.State == "ready" {
		return errors.New("cannot overwrite ready result")
	}
	old := r
	r.State = "failed"
	r.Failure = code
	r.Error = message
	s.records[id] = r
	if err := s.saveLocked(); err != nil {
		s.records[id] = old
		return err
	}
	return nil
}

// Expire first durably records the loss, then removes the body. Active uploads
// are protected; an expired report survives a restart and is retried to master.
func (s *Spool) Expire(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.records {
		if r.State != "ready" || s.busy[id] || now.Before(r.ReadyAt.Add(s.ttl)) {
			continue
		}
		old := r
		r.State = "expired"
		r.Failure = workerprotocol.FailureTimeout
		r.Error = "local result retention expired"
		s.records[id] = r
		if err := s.saveLocked(); err != nil {
			s.records[id] = old
			return err
		}
		if err := os.Remove(s.pdfPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(s.dir)
}
func (s *Spool) BeginUpload(id string) (Record, *os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok || r.State != "ready" || s.busy[id] {
		return r, nil, errors.New("result unavailable")
	}
	f, err := os.Open(s.pdfPath(id))
	if err != nil {
		return r, nil, err
	}
	s.busy[id] = true
	return r, f, nil
}
func (s *Spool) EndUpload(id string) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.busy, id) }

// Archived is the ONLY pre-TTL path that removes a ready PDF. HTTP success,
// staged receipts, unrelated receipts and digest mismatches never qualify.
func (s *Spool) Archived(id string, receipt workerprotocol.Receipt) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return false, nil
	}
	if r.State != "ready" || receipt.State != "done" || receipt.AttemptID != id || receipt.TaskID != r.Assignment.TaskID || receipt.SHA256 != r.SHA256 || receipt.Size != r.Size {
		return false, nil
	}
	delete(s.records, id)
	if err := s.saveLocked(); err != nil {
		s.records[id] = r
		return false, err
	}
	if err := os.Remove(s.pdfPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	return true, syncDir(s.dir)
}
func (s *Spool) Reported(id string, receipt workerprotocol.Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return nil
	}
	if r.State != "failed" && r.State != "expired" {
		return errors.New("result still retained")
	}
	if receipt.AttemptID != id || receipt.TaskID != r.Assignment.TaskID || (receipt.State != "failed" && receipt.State != "expired" && receipt.State != "done") {
		return errors.New("failure report not acknowledged")
	}
	delete(s.records, id)
	if err := s.saveLocked(); err != nil {
		s.records[id] = r
		return err
	}
	return nil
}
