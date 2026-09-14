// Package web resolves one immutable UI filesystem for the shared Web server.
// Git stores source only. Release binaries embed dist; Go module installs fetch
// the exact tagged Release bundle once and verify the cache on later starts.
package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxBundleSize   = 64 << 20
	maxExpandedSize = 256 << 20
	maxFileSize     = 16 << 20
	maxBundleFiles  = 20000
)

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?(\+([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$`)
var pseudoVersion = regexp.MustCompile(`[-.][0-9]{14}-[0-9a-f]{12}(\+incompatible)?$`)

// ReleaseVersion rejects versions that cannot unambiguously name a server
// Release. It never rounds a pseudo-version down or resolves "latest".
func ReleaseVersion(value string) (string, error) {
	value = strings.TrimPrefix(value, "v")
	if !releaseVersion.MatchString(value) || pseudoVersion.MatchString(value) {
		return "", fmt.Errorf("UI requires an exact release version, got %q; install a published vX.Y.Z tag, or build local UI with -tags embedui", value)
	}
	core, _, _ := strings.Cut(value, "+")
	if _, pre, ok := strings.Cut(core, "-"); ok {
		for _, part := range strings.Split(pre, ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return "", errors.New("UI release version has a noncanonical numeric prerelease identifier")
			}
		}
	}
	return value, nil
}

func BundleName(version string) string    { return "qatlasd_" + version + "_web.zip" }
func checksumName(version string) string  { return "qatlasd_" + version + "_checksums.txt" }
func bundleComment(version string) string { return "qatlas-ui-v1:" + version }

func safeBundlePath(name string) bool {
	return name != "." && fs.ValidPath(name) && utf8.ValidString(name) && !strings.ContainsAny(name, "\\:\x00\r\n")
}

// Validate checks the minimum full UI contract, shared by packaging, embedded
// startup and downloaded startup. Documentation is part of the bundle, not an
// optional second download. The dev site uses Sphinx's dev/index root_doc.
func Validate(tree fs.FS) error {
	for _, name := range []string{"index.html", "doc/index.html", "devdoc/dev/index.html"} {
		info, err := fs.Stat(tree, name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("UI bundle is incomplete: missing nonempty %s", name)
		}
	}
	files, err := fs.ReadDir(tree, "assets")
	if err != nil {
		return fmt.Errorf("UI bundle is incomplete: assets: %w", err)
	}
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".js") {
			info, err := file.Info()
			if err == nil && info.Size() > 0 {
				return nil
			}
		}
	}
	return errors.New("UI bundle is incomplete: no JavaScript assets")
}

// Pack writes a deterministic ZIP from the SAME dist tree embedded by the Go
// release build. No generated file is written into the source tree. ZIP comment
// metadata binds the payload to the requested release; the Release checksum
// covers both. WalkDir order, timestamps, modes and compression are fixed.
func Pack(tree fs.FS, version string, out io.Writer) error {
	canonical, err := ReleaseVersion(version)
	if err != nil {
		return err
	}
	version = canonical
	if err := Validate(tree); err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	if err := zw.SetComment(bundleComment(version)); err != nil {
		return err
	}
	var total int64
	count := 0
	err = fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !safeBundlePath(name) {
			return fmt.Errorf("unsafe UI path %q", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		// Empty files (e.g. Sphinx .nojekyll) are legitimate.
		if !info.Mode().IsRegular() || info.Size() > maxFileSize {
			return fmt.Errorf("unsupported UI file %q", name)
		}
		total += info.Size()
		count++
		if total > maxExpandedSize || count > maxBundleFiles {
			return errors.New("UI exceeds bundle limits")
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		f, err := tree.Open(name)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(w, io.LimitReader(f, maxFileSize+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() {
			return fmt.Errorf("UI changed during packaging: %s", name)
		}
		return nil
	})
	closeErr := zw.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// checksumFor accepts the usual SHA256SUMS text syntax, but requires exactly
// one well-formed entry for this asset. It never verifies an unrelated line.
func checksumFor(body []byte, asset string) ([]byte, error) {
	var digest []byte
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 || (strings.TrimPrefix(fields[1], "*") != asset && strings.TrimPrefix(fields[len(fields)-1], "*") != asset) {
			continue
		}
		if digest != nil {
			return nil, errors.New("duplicate UI checksum entry")
		}
		if len(fields) != 2 {
			return nil, errors.New("malformed UI checksum entry")
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return nil, errors.New("invalid UI SHA256 checksum")
		}
		digest = decoded
	}
	if digest == nil {
		return nil, errors.New("UI checksum entry missing")
	}
	return digest, nil
}

// bundleFS keeps validated, immutable expanded bytes so GET/HEAD/Range never
// reinflate files or allocate a full asset per request (PocketBase may Open
// more than once). Every Open gets its own seeker, just like embed.FS.
// The total resident payload is bounded by maxExpandedSize.
type bundleFS struct {
	*zip.Reader
	files map[string]bundleAsset
}
type bundleAsset struct {
	data []byte
	info fs.FileInfo
}

type bundleFile struct {
	*bytes.Reader
	info fs.FileInfo
}

// ZIP's fixed DOS timestamp is NOT a content validator. Exposing it would
// return 304 for changed HTML after an upgrade. Match embed.FS's zero ModTime.
type bundleInfo struct{ fs.FileInfo }

func (bundleInfo) ModTime() time.Time            { return time.Time{} }
func (f *bundleFile) Stat() (fs.FileInfo, error) { return bundleInfo{f.info}, nil }
func (f *bundleFile) Close() error               { return nil }

type bundleEntry struct{ fs.DirEntry }

func (e bundleEntry) Info() (fs.FileInfo, error) {
	info, err := e.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return bundleInfo{info}, nil
}

type bundleDirectory struct{ fs.ReadDirFile }

func (d bundleDirectory) Stat() (fs.FileInfo, error) {
	info, err := d.ReadDirFile.Stat()
	if err != nil {
		return nil, err
	}
	return bundleInfo{info}, nil
}
func (d bundleDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := d.ReadDirFile.ReadDir(n)
	for i, entry := range entries {
		entries[i] = bundleEntry{entry}
	}
	return entries, err
}

func (b bundleFS) Open(name string) (fs.File, error) {
	if asset, ok := b.files[name]; ok {
		return &bundleFile{Reader: bytes.NewReader(asset.data), info: asset.info}, nil
	}
	f, err := b.Reader.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		return bundleDirectory{f.(fs.ReadDirFile)}, nil
	}
	f.Close()
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func openBundle(data, digest []byte, version string) (fs.FS, error) {
	if len(data) > maxBundleSize {
		return nil, errors.New("UI archive exceeds size limit")
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(actual[:], digest) {
		return nil, errors.New("UI SHA256 checksum mismatch")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid UI ZIP: %w", err)
	}
	if zr.Comment != bundleComment(version) {
		return nil, errors.New("UI bundle version/format mismatch")
	}
	if len(zr.File) > maxBundleFiles {
		return nil, errors.New("UI archive has too many entries")
	}
	seen := make(map[string]bool)
	files := make(map[string]bundleAsset)
	var total uint64
	for _, file := range zr.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !safeBundlePath(name) || seen[name] {
			return nil, fmt.Errorf("unsafe or duplicate UI entry %q", name)
		}
		seen[name] = true
		if kind := file.Mode().Type(); kind != 0 && kind != fs.ModeDir {
			return nil, fmt.Errorf("unsupported UI entry %q", name)
		}
		if file.FileInfo().IsDir() {
			if file.UncompressedSize64 != 0 {
				return nil, errors.New("nonempty UI directory entry")
			}
			continue
		}
		if !file.Mode().IsRegular() || file.UncompressedSize64 > maxFileSize {
			return nil, fmt.Errorf("unsupported UI entry %q", name)
		}
		total += file.UncompressedSize64
		if total > maxExpandedSize {
			return nil, errors.New("expanded UI exceeds size limit")
		}
		f, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(f, maxFileSize+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("corrupt UI entry %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if uint64(len(content)) != file.UncompressedSize64 {
			return nil, errors.New("UI entry size mismatch")
		}
		files[name] = bundleAsset{data: content, info: file.FileInfo()}
	}
	// A regular file must not also be a parent of another entry.
	for _, file := range zr.File {
		for parent := path.Dir(strings.TrimSuffix(file.Name, "/")); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				info, err := fs.Stat(zr, parent)
				if err != nil || !info.IsDir() {
					return nil, errors.New("UI file/directory collision")
				}
			}
		}
	}
	tree := bundleFS{Reader: zr, files: files}
	if err := Validate(tree); err != nil {
		return nil, err
	}
	return tree, nil
}
