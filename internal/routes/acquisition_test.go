package routes

import (
	"context"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

func TestPaperAcquisitionPendingFallback(t *testing.T) {
	created := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	body := paperAcquisition(context.Background(), &registry.Paper{
		PaperID: "qa_pending", Status: "pending", CreatedAt: created,
	}, nil, nil, nil)
	if body["state"] != "queued" || body["phase"] != "queued" || body["active"] != true {
		t.Fatalf("body = %+v", body)
	}
	events, ok := body["events"].([]acquisitionEvent)
	if !ok || len(events) != 1 || !events[0].At.Equal(created) {
		t.Fatalf("events = %#v", body["events"])
	}
}

func TestPaperAcquisitionReadyFromMarkdownAsset(t *testing.T) {
	body := paperAcquisition(context.Background(), &registry.Paper{
		PaperID: "qa_ready", DOI: "10.1000/ready", Status: "ready",
	}, []registry.Asset{{
		Source: "published", PDFPath: "doi/10.1000/ready.pdf", MinerUMDPath: "doi/10.1000/ready.md",
	}}, nil, nil)
	if body["state"] != "done" || body["phase"] != "ready" || body["active"] != false {
		t.Fatalf("body = %+v", body)
	}
}
