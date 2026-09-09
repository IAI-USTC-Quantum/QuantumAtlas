package downloadworker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func assignment(id string) workerprotocol.Assignment {
	return workerprotocol.Assignment{TaskID: "task-" + id, AttemptID: id, Ref: workerprotocol.PaperRef{DOI: "10.1234/test"}, LeaseExpires: time.Now().Add(time.Minute), Deadline: time.Now().Add(5 * time.Minute), MaxPDFBytes: MaxPDFBytes}
}
func testSpool(t *testing.T, max int64, ttl time.Duration) *Spool {
	t.Helper()
	s, err := OpenSpool(t.TempDir(), max, ttl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func ready(t *testing.T, s *Spool, id string, now time.Time) Record {
	t.Helper()
	if err := s.Start(assignment(id), 2, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(id, bytes.NewBufferString("%PDF-test body"), workerprotocol.ResultMetadata{Strategy: "test"}, now); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Records() {
		if r.Assignment.AttemptID == id {
			return r
		}
	}
	t.Fatal("missing result")
	return Record{}
}
func receiptFor(r Record, state string) workerprotocol.Receipt {
	return workerprotocol.Receipt{AttemptID: r.Assignment.AttemptID, TaskID: r.Assignment.TaskID, State: state, SHA256: r.SHA256, Size: r.Size}
}

func TestSpoolPersistenceAndInterruptedRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSpool(dir, 3*MaxPDFBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.LoadIdentity("https://master.example")
	if err != nil {
		t.Fatal(err)
	}
	r := ready(t, s, "ready", time.Now())
	if err = s.Start(assignment("running"), 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenSpool(dir, 3*MaxPDFBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	same, err := recovered.LoadIdentity("https://master.example")
	if err != nil || same != identity {
		t.Fatalf("identity changed: %v", err)
	}
	if _, err = recovered.LoadIdentity("https://other.example"); err == nil {
		t.Fatal("credential was allowed on another master")
	}
	records := recovered.Records()
	if len(records) != 2 {
		t.Fatal(records)
	}
	for _, got := range records {
		switch got.Assignment.AttemptID {
		case "ready":
			if got.State != "ready" || got.SHA256 != r.SHA256 {
				t.Fatal(got)
			}
		case "running":
			if got.State != "failed" || got.Failure != workerprotocol.FailureInternal {
				t.Fatal(got)
			}
		}
	}
	info, err := os.Stat(filepath.Join(dir, "identity.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("identity permissions", err)
	}
	if _, err = OpenSpool(dir, 3*MaxPDFBytes, time.Hour); err == nil {
		t.Fatal("duplicate runner acquired volume")
	}
}
func TestReadyRemovedOnlyByMatchingArchivedReceipt(t *testing.T) {
	s := testSpool(t, 2*MaxPDFBytes, time.Hour)
	r := ready(t, s, "attempt", time.Now())
	cases := []workerprotocol.Receipt{receiptFor(r, "staged"), receiptFor(r, "running"), receiptFor(r, "failed"), receiptFor(r, "expired")}
	bad := receiptFor(r, "done")
	bad.SHA256 = "wrong"
	cases = append(cases, bad)
	bad = receiptFor(r, "done")
	bad.AttemptID = "another"
	cases = append(cases, bad)
	bad = receiptFor(r, "done")
	bad.TaskID = "another"
	cases = append(cases, bad)
	bad = receiptFor(r, "done")
	bad.Size++
	cases = append(cases, bad)
	for _, receipt := range cases {
		removed, err := s.Archived("attempt", receipt)
		if err != nil || removed {
			t.Fatalf("bad receipt removed result: %+v %v", receipt, err)
		}
		if _, err = os.Stat(s.pdfPath("attempt")); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Archived("attempt", receiptFor(r, "done"))
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	if _, err = os.Stat(s.pdfPath("attempt")); !os.IsNotExist(err) {
		t.Fatal("body not deleted")
	}
	if again, err := s.Archived("attempt", receiptFor(r, "done")); err != nil || again {
		t.Fatal("second archive not idempotent", err)
	}
	if len(s.Records()) != 0 {
		t.Fatal("record remains")
	}
}
func TestCapacityReservationsAndTTL(t *testing.T) {
	now := time.Now()
	s := testSpool(t, 2*MaxPDFBytes, time.Hour)
	if err := s.Start(assignment("first"), 2, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(assignment("second"), 2, now); err != nil {
		t.Fatal(err)
	}
	if slots := s.Slots(2); slots != 0 {
		t.Fatal("concurrency not reserved", slots)
	}
	if err := s.Start(assignment("third"), 2, now); err == nil {
		t.Fatal("accepted above concurrency")
	}
	if err := s.Ready("first", bytes.NewBufferString("%PDF-one"), workerprotocol.ResultMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	if slots := s.Slots(2); slots != 0 {
		t.Fatal("ready body not charged to quota", slots)
	}
	if err := s.Expire(now.Add(time.Hour - time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.pdfPath("first")); err != nil {
		t.Fatal("unexpired result evicted")
	}
	_, file, err := s.BeginUpload("first")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Expire(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.pdfPath("first")); err != nil {
		t.Fatal("uploading result expired")
	}
	file.Close()
	s.EndUpload("first")
	if err = s.Expire(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.pdfPath("first")); !os.IsNotExist(err) {
		t.Fatal("expired body retained")
	}
	var expired Record
	for _, r := range s.Records() {
		if r.Assignment.AttemptID == "first" {
			expired = r
		}
	}
	if expired.State != "expired" || expired.Failure != workerprotocol.FailureTimeout {
		t.Fatal(expired)
	}
	if err = s.Reported("first", receiptFor(expired, "running")); err == nil {
		t.Fatal("nonterminal report acknowledgement accepted")
	}
	if err = s.Reported("first", receiptFor(expired, "expired")); err != nil {
		t.Fatal(err)
	}
	if slots := s.Slots(2); slots != 1 {
		t.Fatal("expired capacity not released", slots)
	}
}
func TestCorruptReadyDoesNotSilentlyDisappear(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSpool(dir, MaxPDFBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ready(t, s, "attempt", time.Now())
	s.Close()
	if err = os.WriteFile(filepath.Join(dir, "attempt.pdf"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenSpool(dir, MaxPDFBytes, time.Hour); err == nil {
		t.Fatal("corruption accepted")
	}
}
func TestAssignmentPathsAndSizeAreBounded(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	a := assignment("../escape")
	if err := s.Start(a, 2, time.Now()); err == nil {
		t.Fatal("unsafe path accepted")
	}
	a = assignment("small")
	a.MaxPDFBytes = 4
	if err := s.Start(a, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(a.AttemptID, bytes.NewBufferString("12345"), workerprotocol.ResultMetadata{}, time.Now()); err == nil {
		t.Fatal("oversized body accepted")
	}
}
