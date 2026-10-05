package mineru

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io/fs"
	"testing"
)

func testArchive(t *testing.T, headers []zip.FileHeader, contents []string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i, h := range headers {
		w, err := z.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(contents[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCompleteArchivePreservesOriginalUnknownMembers(t *testing.T) {
	r, err := ExtractPackage(buildCompleteZip(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 5 || r.MiddlePath != "middle_json.json" || r.MarkdownPath != "markdown.md" {
		t.Fatalf("package: %+v", r)
	}
	if !bytes.Equal(r.Members["unknown/model.bin"], []byte("\x00raw\xff")) {
		t.Fatal("raw bytes rewritten")
	}
	if string(r.Members["structured_content.json"]) != "{\"schema\":\"consumption\"}" {
		t.Fatal("content view not preserved")
	}
	legacy, err := ExtractResult(buildCompleteZip(t))
	if err != nil || len(legacy.Members) != 5 || len(legacy.MiddleJSON) == 0 {
		t.Fatalf("compat extractor dropped JSON: %v", err)
	}
}

func TestArchivesRejectAnyUnsafeMember(t *testing.T) {
	for _, name := range []string{"../x", "/x", "a/../../x", "a\\b", "C:/x", "a//x", "a/./x", "a/../x", "bad\x00file", "bad\nfile"} {
		t.Run(name, func(t *testing.T) {
			raw := testArchive(t, []zip.FileHeader{{Name: "full.md", Method: zip.Store}, {Name: name, Method: zip.Store}}, []string{"hello", "bad"})
			if _, err := ExtractResult(raw); err == nil {
				t.Fatal("unsafe member accepted")
			}
			if HasMiddleJSON(raw) {
				t.Fatal("unsafe archive recognized")
			}
		})
	}
	symlink := zip.FileHeader{Name: "link", Method: zip.Store}
	symlink.SetMode(fs.ModeSymlink | 0777)
	raw := testArchive(t, []zip.FileHeader{{Name: "full.md", Method: zip.Store}, symlink}, []string{"hello", "full.md"})
	if _, err := ExtractResult(raw); err == nil {
		t.Fatal("symlink accepted")
	}
	directoryLink := zip.FileHeader{Name: "link/", Method: zip.Store}
	directoryLink.SetMode(fs.ModeSymlink | 0777)
	if _, err := ExtractResult(testArchive(t, []zip.FileHeader{{Name: "full.md", Method: zip.Store}, directoryLink}, []string{"hello", ""})); err == nil {
		t.Fatal("directory-named symlink accepted")
	}
	for _, names := range [][]string{{"full.md", "full.md"}, {"full.md", "a", "a/b"}, {"full.md", "a/", "a"}, {"full.md", "a", "a/b/"}} {
		var h []zip.FileHeader
		var c []string
		for _, n := range names {
			h = append(h, zip.FileHeader{Name: n, Method: zip.Store})
			if n == "a/" || n == "a/b/" {
				c = append(c, "")
			} else {
				c = append(c, "x")
			}
		}
		if _, err := ExtractResult(testArchive(t, h, c)); err == nil {
			t.Fatalf("duplicate/collision accepted: %v", names)
		}
	}
}

func TestArchiveResourceAndCRCFailures(t *testing.T) {
	raw := testArchive(t, []zip.FileHeader{{Name: "full.md", Method: zip.Store}}, []string{"unique_crc_marker"})
	broken := append([]byte(nil), raw...)
	i := bytes.Index(broken, []byte("unique_crc_marker"))
	broken[i] = 'X'
	if _, err := ExtractResult(broken); err == nil {
		t.Fatal("CRC corruption accepted")
	}
	oversize := append([]byte(nil), raw...)
	i = bytes.Index(oversize, []byte{'P', 'K', 1, 2})
	binary.LittleEndian.PutUint32(oversize[i+24:i+28], MaxMemberBytes+1)
	if _, err := ExtractResult(oversize); err == nil {
		t.Fatal("declared uncompressed cap ignored")
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := 0; i <= MaxArchiveMembers; i++ {
		w, err := z.Create("f" + string(rune(0x1000+i)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(nil)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractMembers(b.Bytes()); err == nil {
		t.Fatal("member cap ignored")
	}
}

func TestCompleteArchiveRequiresGenuineSupportedMiddle(t *testing.T) {
	for _, middle := range []string{"[]", `{"schema":"content_list","schema_version":"2.0","blocks":[]}`, `{"schema":"docvortex.middle","schema_version":"999","blocks":[]}`, `{"schema":"docvortex.middle","schema_version":"2.0","is_full_document":false,"blocks":[]}`, `{"schema":"docvortex.middle","schema_version":"2.0","blocks":[{"page_idx":0,"index":1},{"page_idx":0,"index":1}]}`} {
		raw := testArchive(t, []zip.FileHeader{{Name: "markdown.md", Method: zip.Store}, {Name: "middle_json.json", Method: zip.Store}}, []string{"hello", middle})
		if _, err := ExtractPackage(raw); err == nil {
			t.Fatal("unsupported JSON reinterpreted as Middle")
		}
	}
	raw := testArchive(t, []zip.FileHeader{{Name: "markdown.md", Method: zip.Store}, {Name: "content_list.json", Method: zip.Store}}, []string{"hello", "[]"})
	if _, err := ExtractPackage(raw); err == nil {
		t.Fatal("ContentList promoted to Middle")
	}
}
