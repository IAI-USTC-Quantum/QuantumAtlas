package mineru

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"
)

// Bounds apply to the complete package, including unknown members. We never
// skip an unsafe member: a rejected archive has no publishable result.
const (
	MaxArchiveBytes      = 128 << 20
	MaxArchiveMembers    = 10000
	MaxMemberBytes       = 128 << 20
	MaxUncompressedBytes = 256 << 20
)

func validArchivePath(name string, directory bool) bool {
	if directory {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
		for _, r := range component {
			if r < 32 || r == 127 {
				return false
			}
		}
	}
	return true
}

// extractMembers preserves each file's original relative path and exact bytes.
// Directory entries are validated but have no persisted byte payload.
func extractMembers(raw []byte) (map[string][]byte, error) {
	if len(raw) > MaxArchiveBytes {
		return nil, archiveError("compressed size exceeds limit")
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, archiveError("open result zip: " + err.Error())
	}
	if len(zr.File) > MaxArchiveMembers {
		return nil, archiveError("member count exceeds limit")
	}
	seen := make(map[string]bool, len(zr.File))
	files := make(map[string][]byte, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		dir := f.FileInfo().IsDir()
		name := strings.TrimSuffix(f.Name, "/")
		if !validArchivePath(f.Name, dir) {
			return nil, archiveError("unsafe member path: " + f.Name)
		}
		if seen[name] {
			return nil, archiveError("duplicate member path: " + f.Name)
		}
		seen[name] = true
		kind := f.Mode() & fs.ModeType
		// A trailing slash can add ModeDir even to a symlink/device header.
		// Only pure directories or regular files are permissible.
		if kind != 0 && kind != fs.ModeDir {
			return nil, archiveError("non-regular member: " + f.Name)
		}
		if dir {
			if f.UncompressedSize64 != 0 {
				return nil, archiveError("directory has data: " + f.Name)
			}
			continue
		}
		if f.UncompressedSize64 > MaxMemberBytes || f.UncompressedSize64 > MaxUncompressedBytes-total {
			return nil, archiveError("uncompressed size exceeds limit: " + f.Name)
		}
		total += f.UncompressedSize64
		rc, err := f.Open()
		if err != nil {
			return nil, archiveError("open member " + f.Name + ": " + err.Error())
		}
		b, readErr := io.ReadAll(io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, archiveError("read member " + f.Name + ": " + readErr.Error())
		}
		if closeErr != nil {
			return nil, archiveError("close member " + f.Name + ": " + closeErr.Error())
		}
		if uint64(len(b)) != f.UncompressedSize64 {
			return nil, archiveError("size mismatch: " + f.Name)
		}
		files[f.Name] = b
	}
	// Reject file/directory aliases (a regular file a alongside a/b), even
	// when the ZIP omitted explicit directory entries.
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return nil, archiveError("file/directory conflict: " + parent)
			}
		}
	}
	return files, nil
}

func archiveError(msg string) error { return &Error{Msg: msg, Kind: ErrFatal} }

func selectArtifact(files map[string][]byte, basenames ...string) (string, error) {
	for _, base := range basenames {
		found := ""
		for name := range files {
			if path.Base(name) == base {
				if found != "" {
					return "", archiveError(fmt.Sprintf("ambiguous %s members", base))
				}
				found = name
			}
		}
		if found != "" {
			return found, nil
		}
	}
	return "", nil
}

func resultFromMembers(files map[string][]byte) (Result, error) {
	res := Result{Members: files, Images: map[string][]byte{}}
	var err error
	res.MarkdownPath, err = selectArtifact(files, "markdown.md", "full.md")
	if err != nil {
		return Result{}, err
	}
	res.MiddlePath, err = selectArtifact(files, "middle_json.json", "layout.json")
	if err != nil {
		return Result{}, err
	}
	res.StructuredContentPath, err = selectArtifact(files, "structured_content.json", "content_list.json")
	if err != nil {
		return Result{}, err
	}
	res.Markdown = files[res.MarkdownPath]
	res.MiddleJSON = files[res.MiddlePath]
	res.StructuredContent = files[res.StructuredContentPath]
	root := path.Dir(res.MarkdownPath)
	if res.MiddlePath != "" {
		root = path.Dir(res.MiddlePath)
	}
	for name, b := range files {
		if IsImageEntry(name) {
			res.Images[RelImageName(name, root)] = b
		}
	}
	return res, nil
}

// ExtractPackage is the production contract: genuine supported Middle JSON
// and markdown must accompany every complete package. ContentList is never
// interpreted as Middle JSON, and member names/bytes are never rewritten.
func ExtractPackage(raw []byte) (Result, error) {
	files, err := extractMembers(raw)
	if err != nil {
		return Result{}, err
	}
	res, err := resultFromMembers(files)
	if err != nil {
		return Result{}, err
	}
	if res.MiddlePath == "" {
		return Result{}, archiveError("complete result has no supported Middle JSON member")
	}
	doc, err := ParseMiddleJSON(res.MiddleJSON)
	if err != nil {
		return Result{}, archiveError("invalid Middle JSON: " + err.Error())
	}
	var completeness struct {
		Full *bool `json:"is_full_document"`
	}
	if err := json.Unmarshal(res.MiddleJSON, &completeness); err != nil {
		return Result{}, archiveError("invalid Middle completeness: " + err.Error())
	}
	if completeness.Full != nil && !*completeness.Full {
		return Result{}, archiveError("partial Middle document is not a complete result")
	}
	anchors := make(map[[2]int]bool, len(doc.Blocks))
	for _, block := range doc.Blocks {
		anchor := [2]int{block.PageIdx, block.Index}
		if block.PageIdx < 0 || block.Index < 1 || doc.Pages > 0 && block.PageIdx >= doc.Pages || anchors[anchor] {
			return Result{}, archiveError("invalid or duplicate Middle block anchor")
		}
		anchors[anchor] = true
	}
	if res.MarkdownPath == "" {
		return Result{}, archiveError("complete result has no markdown member")
	}
	return res, nil
}
