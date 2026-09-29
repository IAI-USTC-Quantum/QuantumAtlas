package mineru

// newformat.go: reader for the NEW MinerU result-zip layout (the
// "Doclib" format, plan §5): middle_json.json + markdown.md + images/
// instead of the legacy single full.md. Detection rule mirrors the
// legacy full.md rule in client.go — any zip entry whose BASENAME is
// exactly "middle_json.json" (the file may sit under a per-paper
// subdirectory inside the zip, like full.md does).
//
// What the ingest needs out of a new-format zip:
//
//   - MiddleJSON: the docvortex.middle artifact (validated by the
//     caller via ParseMiddleJSON before anything is persisted — plan
//     Q1: 坏 schema 如实报错, never silently treated as markdown).
//   - Markdown: markdown.md when present; falls back to full.md for
//     hybrid zips (some toolchains ship both). nil when neither —
//     middle JSON is the only mandatory member.
//   - Images: every images/ member, keyed relative to the zip root
//     (same keying as the legacy path).
//   - Tier: read from a metadata.json member shaped {"tier": "..."}
//     when the producer recorded one; "" when absent (caller defaults
//     to registry.ParseTierDefault).

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"path"
	"strings"
)

// NewFormatResult is the parsed content of a new-format MinerU zip.
type NewFormatResult struct {
	MiddleJSON []byte
	Markdown   []byte // markdown.md / full.md member; nil when absent
	Images     map[string][]byte
	Tier       string // "" when the zip carries no tier metadata
}

// middleJSONBase is the basename that identifies the new-format parse
// artifact inside a result zip.
const middleJSONBase = "middle_json.json"

// metadataJSONBase is the basename of the optional producer-metadata
// member the tier is read from.
const metadataJSONBase = "metadata.json"

// HasMiddleJSON reports whether the zip contains a new-format
// middle_json.json member (any directory depth). Cheap pre-check so
// the legacy upload path can branch without slurping members.
func HasMiddleJSON(zipBytes []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && path.Base(f.Name) == middleJSONBase {
			return true
		}
	}
	return false
}

// ExtractNewFormat parses a new-format MinerU result zip. Returns an
// Error (client-class) when the zip has no middle_json.json member at
// all — callers gate on HasMiddleJSON first, so hitting that branch
// means a detection/extract race on a mutated buffer.
func ExtractNewFormat(zipBytes []byte) (NewFormatResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return NewFormatResult{}, &Error{Msg: "open result zip: " + err.Error()}
	}

	res := NewFormatResult{Images: map[string][]byte{}}
	var middleName, mdName string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		switch base := path.Base(f.Name); {
		case base == middleJSONBase:
			// First middle_json.json wins; a zip carrying two is a
			// producer bug we don't try to outguess.
			if middleName == "" {
				middleName = f.Name
			}
		case base == "markdown.md":
			if mdName == "" {
				mdName = f.Name
			}
		case base == "full.md":
			// Hybrid zips: only use full.md as markdown when no
			// markdown.md was found (checked again post-scan below).
			if mdName == "" {
				mdName = f.Name
			}
		}
	}
	if middleName == "" {
		return NewFormatResult{}, &Error{Msg: "result zip did not contain " + middleJSONBase}
	}
	// Root for relative image refs: the middle JSON's directory.
	midDir := path.Dir(middleName)

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Name == middleName {
			continue
		}
		base := path.Base(f.Name)
		switch {
		case f.Name == mdName && (base == "markdown.md" || base == "full.md"):
			b, err := readZipEntry(f)
			if err != nil {
				return NewFormatResult{}, err
			}
			res.Markdown = b
		case base == metadataJSONBase:
			// Optional producer metadata; tolerate unreadable/corrupt
			// members by ignoring them (tier falls back to default).
			if b, err := readZipEntry(f); err == nil {
				var meta struct {
					Tier string `json:"tier"`
				}
				if json.Unmarshal(b, &meta) == nil && strings.TrimSpace(meta.Tier) != "" {
					res.Tier = meta.Tier
				}
			}
		case IsImageEntry(f.Name):
			b, err := readZipEntry(f)
			if err != nil {
				return NewFormatResult{}, err
			}
			res.Images[RelImageName(f.Name, midDir)] = b
		}
	}

	b, err := readZipEntry(mustFind(zr, middleName))
	if err != nil {
		return NewFormatResult{}, err
	}
	res.MiddleJSON = b
	return res, nil
}

// mustFind re-locates the named member (the first pass kept only its
// name). Returns nil when absent — readZipEntry(nil) would panic, so
// callers treat that as the not-found error again.
func mustFind(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && f.Name == name {
			return f
		}
	}
	return nil
}
