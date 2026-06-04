package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// buildResultZip returns an in-memory MinerU-style result zip containing a
// "<prefix>/full.md" and one image under "<prefix>/images/".
func buildResultZip(t *testing.T, prefix, md string, images map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mdName := "full.md"
	if prefix != "" {
		mdName = prefix + "/full.md"
	}
	w, err := zw.Create(mdName)
	if err != nil {
		t.Fatalf("zip create md: %v", err)
	}
	if _, err := w.Write([]byte(md)); err != nil {
		t.Fatalf("zip write md: %v", err)
	}
	for name, content := range images {
		entry := name
		if prefix != "" {
			entry = prefix + "/" + name
		}
		iw, err := zw.Create(entry)
		if err != nil {
			t.Fatalf("zip create img: %v", err)
		}
		if _, err := iw.Write([]byte(content)); err != nil {
			t.Fatalf("zip write img: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestSubmitURLTask(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/extract/task" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"task_id":"task-42"}}`))
	}))
	defer srv.Close()

	c := NewClient("secret-token", srv.URL, srv.Client())
	id, err := c.SubmitURLTask(context.Background(), "https://example.com/x.pdf", SubmitOptions{
		ModelVersion:  "vlm",
		Language:      "ch",
		EnableFormula: true,
		EnableTable:   true,
	})
	if err != nil {
		t.Fatalf("SubmitURLTask: %v", err)
	}
	if id != "task-42" {
		t.Fatalf("task id = %q, want task-42", id)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["url"] != "https://example.com/x.pdf" {
		t.Fatalf("url = %v", gotBody["url"])
	}
	if gotBody["enable_formula"] != true {
		t.Fatalf("enable_formula = %v", gotBody["enable_formula"])
	}
}

func TestSubmitURLTaskEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":-1,"msg":"quota exceeded","data":null}`))
	}))
	defer srv.Close()

	c := NewClient("t", srv.URL, srv.Client())
	_, err := c.SubmitURLTask(context.Background(), "https://x", SubmitOptions{})
	if err == nil {
		t.Fatal("expected error on non-zero code")
	}
	var me *Error
	if !asMineruError(err, &me) {
		t.Fatalf("error type = %T, want *mineru.Error", err)
	}
}

func TestGetTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/extract/task/task-42" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"state":"done","full_zip_url":"https://z/x.zip"}}`))
	}))
	defer srv.Close()

	c := NewClient("t", srv.URL, srv.Client())
	st, err := c.GetTask(context.Background(), "task-42")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if st.State != "done" || st.FullZipURL != "https://z/x.zip" {
		t.Fatalf("state = %+v", st)
	}
}

func TestFetchResult(t *testing.T) {
	const md = "# Title\n\n![fig](images/fig1.png)\n"
	zipBytes := buildResultZip(t, "result", md, map[string]string{
		"images/fig1.png": "PNGDATA",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	c := NewClient("t", "", srv.Client())
	res, err := c.FetchResult(context.Background(), srv.URL+"/x.zip")
	if err != nil {
		t.Fatalf("FetchResult: %v", err)
	}
	if string(res.Markdown) != md {
		t.Fatalf("markdown = %q", res.Markdown)
	}
	img, ok := res.Images["images/fig1.png"]
	if !ok {
		t.Fatalf("image images/fig1.png missing; got keys %v", keysOf(res.Images))
	}
	if string(img) != "PNGDATA" {
		t.Fatalf("image content = %q", img)
	}
}

func TestExtractResultNoMarkdown(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("notes.txt")
	_, _ = w.Write([]byte("nope"))
	_ = zw.Close()

	_, err := ExtractResult(buf.Bytes())
	if err == nil {
		t.Fatal("expected error when zip has no full.md")
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func asMineruError(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

func TestSubmitURLBatch(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"batch_id":"batch-7"}}`))
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, srv.Client())
	id, err := c.SubmitURLBatch(context.Background(), []BatchFile{
		{URL: "https://example.com/a.pdf", DataID: "paper-a"},
		{URL: "https://example.com/b.pdf", DataID: "paper-b"},
	}, SubmitOptions{ModelVersion: "vlm", Language: "ch", EnableFormula: true, EnableTable: true})
	if err != nil {
		t.Fatalf("SubmitURLBatch: %v", err)
	}
	if id != "batch-7" {
		t.Fatalf("batch_id = %q", id)
	}
	if gotPath != "/api/v4/extract/task/batch" {
		t.Fatalf("path = %q", gotPath)
	}
	files, ok := gotBody["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("files = %v", gotBody["files"])
	}
	first := files[0].(map[string]any)
	if first["url"] != "https://example.com/a.pdf" || first["data_id"] != "paper-a" {
		t.Fatalf("first file = %v", first)
	}
	if gotBody["model_version"] != "vlm" {
		t.Fatalf("model_version = %v", gotBody["model_version"])
	}
}

func TestSubmitURLBatchTooBig(t *testing.T) {
	// No HTTP server: validation should fire *before* any request goes out.
	c := NewClient("tok", "http://nowhere.invalid", nil)
	files := make([]BatchFile, MaxBatchSize+1)
	for i := range files {
		files[i] = BatchFile{URL: "https://example.com/x.pdf", DataID: "d"}
	}
	_, err := c.SubmitURLBatch(context.Background(), files, SubmitOptions{})
	if err == nil {
		t.Fatal("expected error for oversized batch")
	}
	var me *Error
	if !asMineruError(err, &me) {
		t.Fatalf("error type = %T", err)
	}
}

func TestSubmitURLBatchEmpty(t *testing.T) {
	c := NewClient("tok", "http://nowhere.invalid", nil)
	_, err := c.SubmitURLBatch(context.Background(), nil, SubmitOptions{})
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestSubmitURLBatchDailyLimit(t *testing.T) {
	// MinerU returns code -60018 mid-batch when quota is exhausted; we want
	// it classified so the daemon can sleep until tomorrow rather than spin.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":-60018,"msg":"每日解析任务数量已达上限","data":null}`))
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, srv.Client())
	_, err := c.SubmitURLBatch(context.Background(), []BatchFile{{URL: "https://example.com/x.pdf"}}, SubmitOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("expected ErrDailyLimit, got %v", err)
	}
}

func TestGetBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/extract-results/batch/batch-7" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"code": 0,
			"data": {
				"batch_id": "batch-7",
				"extract_result": [
					{"file_name":"a.pdf","data_id":"paper-a","state":"done","full_zip_url":"https://z/a.zip"},
					{"file_name":"b.pdf","data_id":"paper-b","state":"running","extract_progress":{"extracted_pages":12,"total_pages":40,"start_time":"2026-05-31 10:00:00"}},
					{"file_name":"c.pdf","data_id":"paper-c","state":"failed","err_msg":"corrupted pdf"}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, srv.Client())
	results, err := c.GetBatch(context.Background(), "batch-7")
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d", len(results))
	}
	if results[0].State != "done" || results[0].FullZipURL != "https://z/a.zip" || results[0].DataID != "paper-a" {
		t.Fatalf("results[0] = %+v", results[0])
	}
	if results[1].ExtractProgress.ExtractedPages != 12 || results[1].ExtractProgress.TotalPages != 40 {
		t.Fatalf("results[1] progress = %+v", results[1].ExtractProgress)
	}
	if results[2].State != "failed" || results[2].ErrMsg != "corrupted pdf" {
		t.Fatalf("results[2] = %+v", results[2])
	}
}

func TestGetBatchEmptyResults(t *testing.T) {
	// MinerU sometimes accepts a batch and then has nothing to report yet
	// — extract_result can be null. We must return (nil, nil), not crash.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"batch_id":"batch-7","extract_result":null}}`))
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, srv.Client())
	results, err := c.GetBatch(context.Background(), "batch-7")
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %+v", results)
	}
}

func TestGetBatchEmptyID(t *testing.T) {
	c := NewClient("tok", "http://nowhere.invalid", nil)
	_, err := c.GetBatch(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty batch id")
	}
}

// TestBuildImagesZip_Deterministic guards the upload-mineru idempotency
// contract: re-uploading the same MinerU result must produce the same
// sha256, otherwise the second upload hits 409 (different content) instead
// of 200 (unchanged). Two failure modes used to break this:
//
//  1. Go map iteration is randomized per-map, so a naive `for rel, data
//     := range result.Images` wrote entries in a different order each
//     call → different zip central directory → different sha256.
//  2. The server-side converter set zip mtime to time.Now(), injecting
//     wall-clock into the byte stream.
//
// BuildImagesZip fixes both. This test builds the zip from two distinct
// map literals with the same content (forcing different random seeds)
// many times in a row and asserts every result is byte-identical.
func TestBuildImagesZip_Deterministic(t *testing.T) {
	want := map[string][]byte{
		"a.jpg": []byte("alpha-bytes"),
		"b.png": []byte("bravo-bytes-xx"),
		"c.gif": []byte("charlie"),
		"d.jpg": []byte("delta-payload-of-some-length"),
		"e.png": []byte("echo"),
	}

	first, err := BuildImagesZip(want)
	if err != nil {
		t.Fatalf("BuildImagesZip first: %v", err)
	}

	// 32 attempts; with 5 entries, the probability that randomized map
	// iteration happens to land on the sorted order every time is
	// (1/120)**32, well under any test-flake threshold. Each iteration
	// rebuilds the input map from a fresh literal so Go gives it a new
	// per-map random seed.
	for i := 0; i < 32; i++ {
		again := map[string][]byte{
			"e.png": []byte("echo"),
			"c.gif": []byte("charlie"),
			"a.jpg": []byte("alpha-bytes"),
			"d.jpg": []byte("delta-payload-of-some-length"),
			"b.png": []byte("bravo-bytes-xx"),
		}
		got, err := BuildImagesZip(again)
		if err != nil {
			t.Fatalf("BuildImagesZip rebuild %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("zip bytes differ on rebuild %d — map-iteration randomness leaks into output", i)
		}
	}
}

// TestBuildImagesZip_StripsImagesPrefix asserts that the "images/" prefix
// MinerU emits is stripped before writing the entry name, so the stored
// zip layout stays flat ("a.jpg", not "images/a.jpg") regardless of how
// the upstream key was spelled.
func TestBuildImagesZip_StripsImagesPrefix(t *testing.T) {
	data, err := BuildImagesZip(map[string][]byte{
		"images/a.jpg": []byte("a"),
		"b.png":        []byte("b"),
	})
	if err != nil {
		t.Fatalf("BuildImagesZip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	gotNames := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		gotNames = append(gotNames, f.Name)
	}
	// Entries are sorted by canonical (post-strip) name.
	wantNames := []string{"a.jpg", "b.png"}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("entry count = %d, want %d (names=%v)", len(gotNames), len(wantNames), gotNames)
	}
	for i, n := range wantNames {
		if gotNames[i] != n {
			t.Errorf("entry %d = %q, want %q", i, gotNames[i], n)
		}
	}
}

// TestBuildImagesZip_DropsTraversal asserts that "../" entries are
// silently dropped — matches the prior inline-loop behavior at both
// callers (routes/papers.go and mineru/converter.go).
func TestBuildImagesZip_DropsTraversal(t *testing.T) {
	data, err := BuildImagesZip(map[string][]byte{
		"a.jpg":           []byte("a"),
		"../etc/passwd":   []byte("nope"),
		"images/..":       []byte("nope"),
		"images/../b.jpg": []byte("nope"),
	})
	if err != nil {
		t.Fatalf("BuildImagesZip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "a.jpg" {
		names := make([]string, 0, len(zr.File))
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		t.Fatalf("expected only [a.jpg], got %v", names)
	}
}
