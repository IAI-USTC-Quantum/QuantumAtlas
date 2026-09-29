package routes

// papers_mineru_ingest_real_test.go: re-verification of the new-format
// ingest against a GENUINE MinerU 4.x result zip. Opt-in via env:
//
//	QATLAS_REAL_MINERU_ZIP=/path/to/full.zip
//
// Unset → skip with an explicit pending marker (same contract as
// internal/mineru/realartifact_test.go). Drives ingestMinerUNewFormat
// over a real zip + fake catalog + LocalStore and asserts the bundle
// layout, then reads the combined block back through the stored
// artifact bytes.
import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

func TestRealMinerUIngestEndToEnd(t *testing.T) {
	zipPath := os.Getenv("QATLAS_REAL_MINERU_ZIP")
	if zipPath == "" {
		t.Skip("QATLAS_REAL_MINERU_ZIP unset; real-ingest re-verification pending (synthetic tests only)")
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read %s: %v", zipPath, err)
	}

	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	c := &fakeIngestCatalog{
		byArxiv:   map[string]string{"2501.09999": fixturePaperID},
		sources:   map[string][]registry.PaperSource{},
		revisions: map[string][]registry.ParseRevision{},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/papers/2501.09999v1/upload-mineru", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	pdfSha := strings.Repeat("77", 32)
	if err := ingestMinerUNewFormat(re, store, c, "2501.09999v1", zipBytes,
		pdfSha, "", "real-verify", "realpaper"); err != nil {
		t.Fatalf("ingest genuine zip: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	// Bundle members exist in the store.
	keys := body["objstore_keys"].(map[string]any)
	midKey := keys["middle_json"].(string)
	if mid, err := readAllFromStore(t, store, midKey); err != nil || len(mid) == 0 {
		t.Fatalf("stored middle.json unreadable: %v", err)
	}
	if imgs, ok := keys["images"].([]any); !ok || len(imgs) == 0 {
		t.Log("note: no images in this zip")
	} else {
		for _, k := range imgs {
			if _, err := readAllFromStore(t, store, k.(string)); err != nil {
				t.Fatalf("stored image %v unreadable: %v", k, err)
			}
		}
	}

	// The revision row points at the stored bytes and parses.
	revs := c.revisions[fixturePaperID]
	if len(revs) != 1 {
		t.Fatalf("revisions = %d, want 1", len(revs))
	}
	revBytes, err := readAllFromStore(t, store, revs[0].ObjstoreKey)
	if err != nil {
		t.Fatalf("revision key unreadable: %v", err)
	}
	revSum := sha256Bytes(revBytes)
	if hex.EncodeToString(revSum[:]) != revs[0].ArtifactSha256 {
		t.Error("artifact_sha256 does not match the stored genuine middle.json")
	}

	// 00009 tier round-trip: no tier metadata in a genuine zip → default.
	if revs[0].Tier != "standard" {
		t.Errorf("tier = %q, want standard (genuine zips carry no tier metadata)", revs[0].Tier)
	}

	// Source row pinned to the claimed PDF sha.
	src := c.sources[fixturePaperID]
	if len(src) != 1 || src[0].Sha256 != pdfSha {
		t.Errorf("source rows = %+v", src)
	}

	// Locator chain over real data: the stored artifact's first
	// normalized block + the source sha render a well-formed locator
	// (doc:<7hex>/tier:standard/page:1/block:1).
	doc, err := mineru.ParseMiddleJSON(revBytes)
	if err != nil {
		t.Fatalf("re-parse stored artifact: %v", err)
	}
	first := doc.OrderedBlocks()[0]
	got := mineru.Locator(src[0].Sha256, revs[0].Tier, first.PageIdx, first.Index)
	want := "doc:" + pdfSha[:7] + "/tier:standard/page:1/block:1"
	if got != want {
		t.Errorf("locator = %q, want %q", got, want)
	}

	t.Logf("real ingest OK: revision=%s tier=%s images=%d locator=%q",
		revs[0].RevisionID, revs[0].Tier, len(keys["images"].([]any)), got)
	_ = context.Background()
}
