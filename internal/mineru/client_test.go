package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
