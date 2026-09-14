package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests never inherit service targets, credentials, proxy settings or
// HOME. Even a regression in endpoint validation cannot reach the Internet:
// the only HTTP executables on PATH are wrappers that reject non-fixture URLs
// and automatic redirects before invoking the real curl/wget.
const installerOld = "#!/bin/sh\nprintf 'old binary\\n'\n"
const installerConfig = "# private fixture config: must not be changed\nauth: fixture-only\n"

type installerEntry struct {
	name, body, link string
	kind             byte
}

func installerProgram(version string) string {
	return "#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = --version ] || exit 19\nprintf '%s\\n' 'qatlasd version " + version + "'\n"
}

func installerArchive(t *testing.T, entries []installerEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: entry.name, Typeflag: kind, Mode: 0o755, Linkname: entry.link}
		if kind == tar.TypeReg {
			hdr.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := io.WriteString(tw, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

type installerOptions struct {
	version, platform, client, httpFault, checksumFault, toolFault string
	archiveFault                                                   string
	entries                                                        []installerEntry
	args, env                                                      []string
	readonly, pipe, fresh, busybox                                 bool
}

type installerRun struct {
	root, home, bin, dest, tmp, config, script, client, serverURL string
	env                                                           []string
	options                                                       installerOptions
	requests                                                      atomic.Int32
}

func installerWrite(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func installerQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func newInstallerRun(t *testing.T, options installerOptions) *installerRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("installer is POSIX-only")
	}
	if options.version == "" {
		options.version = "1.2.3"
	}
	if options.platform == "" {
		options.platform = "linux/amd64"
	}
	if options.client == "" {
		options.client = "curl"
	}
	clientPath, err := exec.LookPath(options.client)
	if err != nil {
		t.Skipf("%s unavailable: %v", options.client, err)
	}
	if options.client == "wget" {
		cmd := exec.Command(clientPath, "--help")
		cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("--max-redirect")) {
			t.Skip("GNU wget is unavailable")
		}
	}
	r := &installerRun{root: t.TempDir(), options: options, client: options.client}
	r.home = filepath.Join(r.root, "home")
	r.bin = filepath.Join(r.root, "tools")
	r.dest = filepath.Join(r.root, "install with spaces")
	r.tmp = filepath.Join(r.root, "tmp")
	r.config = filepath.Join(r.home, ".qatlas", "config.yaml")
	r.script = filepath.Join(composeRoot(t), "cmd", "qatlasd", "install-qatlasd.sh")
	for _, dir := range []string{r.home, r.bin, r.dest, r.tmp, filepath.Dir(r.config)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !options.fresh {
		installerWrite(t, filepath.Join(r.dest, "qatlasd"), installerOld, 0o755)
	}
	installerWrite(t, r.config, installerConfig, 0o600)
	installerWrite(t, filepath.Join(r.dest, "config.yaml"), installerConfig, 0o600)

	// Only explicitly enumerated, non-network tools enter PATH. In particular,
	// there is no sudo, Python, package manager or second HTTP client fallback.
	for _, tool := range []string{"tar", "awk", "mktemp", "chmod", "mv", "mkdir", "rm", "sleep", "uname", "gzip", "sh", "cat", "cmp", "sha256sum"} {
		path, err := exec.LookPath(tool)
		if err != nil && tool == "sha256sum" {
			tool = "shasum"
			path, err = exec.LookPath(tool)
		}
		if err != nil {
			t.Fatalf("required POSIX fixture dependency %s: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(r.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	platform := strings.SplitN(options.platform, "/", 2)
	osName := map[string]string{"linux": "Linux", "darwin": "Darwin"}[platform[0]]
	if osName == "" {
		osName = platform[0]
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[platform[1]]
	if arch == "" {
		arch = platform[1]
	}
	r.replaceTool(t, "uname", "case \"$1\" in -s) echo "+installerQuote(osName)+" ;; -m) echo "+installerQuote(arch)+" ;; *) exit 9 ;; esac\n")
	if options.busybox {
		busybox, err := exec.LookPath("busybox")
		if err != nil {
			t.Skip("BusyBox unavailable")
		}
		for _, tool := range []string{"tar", "awk", "mktemp", "sh"} {
			r.replaceTool(t, tool, "exec "+installerQuote(busybox)+" "+tool+" \"$@\"\n")
		}
	}
	entries := options.entries
	if entries == nil {
		entries = []installerEntry{
			{name: "qatlasd", body: installerProgram(options.version)},
			{name: "LICENSE", body: "fixture license\n"},
			{name: "docs/", kind: tar.TypeDir},
			{name: "docs/README.md", body: "fixture documentation\n"},
		}
	}
	archive := installerArchive(t, entries)
	switch options.archiveFault {
	case "truncated-gzip":
		archive = archive[:len(archive)-8]
	case "not-gzip":
		archive = []byte("not a gzip archive")
	case "not-tar":
		var data bytes.Buffer
		gz := gzip.NewWriter(&data)
		if _, err := gz.Write(bytes.Repeat([]byte{'x'}, 2048)); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		archive = data.Bytes()
	}
	asset := "qatlasd_" + options.version + "_" + strings.ReplaceAll(options.platform, "/", "_") + ".tar.gz"
	checksums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), asset)
	switch options.checksumFault {
	case "wrong":
		checksums = strings.Repeat("0", 64) + "  " + asset + "\n"
	case "missing":
		checksums = strings.Repeat("0", 64) + "  other.tar.gz\n"
	case "empty":
		checksums = ""
	case "duplicate":
		checksums += checksums
	case "conflicting":
		checksums += strings.Repeat("0", 64) + " *" + asset + "\n"
	case "short":
		checksums = "abcd  " + asset + "\n"
	case "nonhex":
		checksums = strings.Repeat("g", 64) + "  " + asset + "\n"
	case "extra-field":
		checksums = strings.TrimSpace(checksums) + " trailing\n"
	case "binary-marker":
		checksums = strings.ReplaceAll(checksums, "  ", " *")
	case "upper":
		checksums = strings.ToUpper(checksums[:64]) + checksums[64:]
	}
	prefix := "/fixture/repository/releases/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests.Add(1)
		if req.URL.Path == prefix+"latest" {
			if options.httpFault == "latest-unresolved" {
				fmt.Fprint(w, "not a release")
				return
			}
			http.Redirect(w, req, prefix+"tag/v"+options.version, http.StatusFound)
			return
		}
		if req.URL.Path == prefix+"tag/v"+options.version {
			fmt.Fprint(w, "fixture release")
			return
		}
		var payload []byte
		switch req.URL.Path {
		case prefix + "download/v" + options.version + "/" + asset:
			payload = archive
		case prefix + "download/v" + options.version + "/qatlasd_" + options.version + "_checksums.txt":
			payload = []byte(checksums)
		default:
			t.Errorf("unexpected fixture request: %s", req.URL.Path)
			http.NotFound(w, req)
			return
		}
		if strings.HasSuffix(req.URL.Path, ".tar.gz") || options.httpFault == "checksum-404" {
			switch options.httpFault {
			case "404", "checksum-404":
				http.NotFound(w, req)
				return
			case "disconnect":
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)+100))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(payload[:len(payload)/2])
				return
			case "timeout":
				select {
				case <-req.Context().Done():
				case <-time.After(4 * time.Second):
				}
				return
			case "remote-redirect":
				http.Redirect(w, req, "https://example.invalid/never-contact", http.StatusFound)
				return
			case "loop-redirect":
				http.Redirect(w, req, req.URL.Path, http.StatusFound)
				return
			case "corrupt":
				payload = []byte("corrupt download")
			}
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	r.serverURL = server.URL
	wrapper := "for arg do\n case \"$arg\" in -L|--location|--location-trusted) echo 'fixture forbids automatic redirects' >&2; exit 95 ;; esac\n last=$arg\ndone\n"
	if options.client == "wget" {
		wrapper += "if [ \"$#\" -eq 1 ] && [ \"$1\" = --help ]; then exec " + installerQuote(clientPath) + " --help; fi\n"
	}
	wrapper += "case \"$last\" in " + installerQuote(server.URL+"/") + "*) ;; *) echo 'fixture blocked non-loopback request' >&2; exit 96 ;; esac\n"
	wrapper += "exec " + installerQuote(clientPath) + " \"$@\"\n"
	installerWrite(t, filepath.Join(r.bin, options.client), "#!/bin/sh\n"+wrapper, 0o755)
	r.env = []string{
		"PATH=" + r.bin, "HOME=" + r.home, "TMPDIR=" + r.tmp, "LC_ALL=C",
		"QATLAS_REPO=fixture/repository", "QATLAS_VERSION=v" + options.version,
		"QATLAS_INSTALL_DIR=" + r.dest, "QATLAS_INSTALL_TEST_BASE_URL=" + server.URL,
		"QATLAS_INSTALL_TIMEOUT=1", "QATLAS_INSTALL_VERSION_TIMEOUT=1",
		// The installer must ignore options that could change tar's semantics.
		"TAR_OPTIONS=--this-option-must-not-be-used", "GZIP=--invalid",
	}
	for _, pair := range options.env {
		r.setEnv(pair)
	}
	switch options.toolFault {
	case "rename":
		r.replaceTool(t, "mv", "echo 'fixture rename denied' >&2; exit 73\n")
	case "chmod":
		r.replaceTool(t, "chmod", "echo 'fixture chmod denied' >&2; exit 73\n")
	case "noexec":
		chmod, _ := exec.LookPath("chmod")
		r.replaceTool(t, "chmod", "exec "+installerQuote(chmod)+" 0600 \"$2\"\n")
	case "extract":
		tarPath, _ := exec.LookPath("tar")
		r.replaceTool(t, "tar", "case \"$1\" in -x*) printf 'partial binary'; exit 73 ;; esac\nexec "+installerQuote(tarPath)+" \"$@\"\n")
	case "destination-directory":
		if err := os.Remove(filepath.Join(r.dest, "qatlasd")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(r.dest, "qatlasd"), 0o700); err != nil {
			t.Fatal(err)
		}
	case "destination-symlink":
		if err := os.Rename(filepath.Join(r.dest, "qatlasd"), filepath.Join(r.dest, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("original", filepath.Join(r.dest, "qatlasd")); err != nil {
			t.Fatal(err)
		}
	}
	if options.readonly {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory mode permissions")
		}
		if err := os.Chmod(r.dest, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(r.dest, 0o700) })
	}
	return r
}

func (r *installerRun) setEnv(pair string) {
	key := strings.SplitN(pair, "=", 2)[0] + "="
	for i, old := range r.env {
		if strings.HasPrefix(old, key) {
			r.env[i] = pair
			return
		}
	}
	r.env = append(r.env, pair)
}

func (r *installerRun) replaceTool(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join(r.bin, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	installerWrite(t, path, "#!/bin/sh\n"+body, 0o755)
}

func (r *installerRun) run(t *testing.T, wantError string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{r.script}
	if r.options.pipe {
		args = []string{"-s", "--"}
	}
	args = append(args, r.options.args...)
	cmd := exec.CommandContext(ctx, filepath.Join(r.bin, "sh"), args...)
	cmd.Env = r.env
	cmd.Dir = r.root
	cmd.WaitDelay = time.Second
	if r.options.pipe {
		data, err := os.ReadFile(r.script)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = bytes.NewReader(data)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("installer escaped its timeout: %v\n%s", ctx.Err(), output)
	}
	if wantError == "" {
		if err != nil {
			t.Fatalf("installer failed: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "qatlasd config init") {
			t.Errorf("missing safe config-init guidance: %s", output)
		}
	} else if err == nil || !strings.Contains(string(output), wantError) {
		t.Fatalf("want failure containing %q, got %v\n%s", wantError, err, output)
	}
	for _, path := range []string{r.config, filepath.Join(r.dest, "config.yaml")} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != installerConfig {
			t.Errorf("existing config changed at %s: %q, %v", path, data, err)
		}
	}
	if wantError != "" && r.options.toolFault != "destination-directory" {
		data, err := os.ReadFile(filepath.Join(r.dest, "qatlasd"))
		if r.options.fresh {
			if !os.IsNotExist(err) {
				t.Errorf("failed fresh install left a binary: %q, %v", data, err)
			}
		} else if err != nil || string(data) != installerOld {
			t.Errorf("failed upgrade damaged old binary: %q, %v", data, err)
		}
	}
	files, err := os.ReadDir(r.dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".qatlasd-install.") || file.Name() == "LICENSE" || file.Name() == "docs" {
			t.Errorf("unexpected extracted/staging entry left in install directory: %s", file.Name())
		}
	}
	files, err = os.ReadDir(r.tmp)
	if err != nil || len(files) != 0 {
		t.Errorf("download staging leaked: %v, %v", files, err)
	}
	return string(output)
}

func TestInstallerSuccess(t *testing.T) {
	for _, options := range []installerOptions{
		{},
		{fresh: true, pipe: true},
		{platform: "linux/arm64"},
		{platform: "darwin/arm64"},
		{version: "1.2.3-rc.1+fixture", pipe: true},
		{args: []string{"--version", "1.2.3"}, env: []string{"QATLAS_VERSION=wrong-env"}},
		{env: []string{"QATLAS_VERSION=latest"}},
		{checksumFault: "binary-marker"},
		{checksumFault: "upper"},
		{client: "wget", env: []string{"QATLAS_VERSION=latest"}},
		{busybox: true, pipe: true},
	} {
		t.Run(fmt.Sprintf("%+v", options), func(t *testing.T) {
			r := newInstallerRun(t, options)
			if len(options.args) > 0 {
				r.options.args = append(r.options.args, "--dir", r.dest)
				r.setEnv("QATLAS_INSTALL_DIR=" + filepath.Join(r.root, "unused"))
			}
			// A hardlink observes the old inode: truncation/copy-in-place is not
			// atomic replacement even when final bytes happen to be correct.
			oldLink := filepath.Join(r.root, "old-inode")
			if !options.fresh {
				if err := os.Link(filepath.Join(r.dest, "qatlasd"), oldLink); err != nil {
					t.Fatal(err)
				}
			}
			r.run(t, "")
			if !options.fresh {
				data, err := os.ReadFile(oldLink)
				if err != nil || string(data) != installerOld {
					t.Fatalf("upgrade modified old inode in place: %q, %v", data, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(r.dest, "qatlasd"))
			if err != nil || string(data) != installerProgram(r.options.version) {
				t.Fatalf("installed wrong binary: %q, %v", data, err)
			}
			info, err := os.Stat(filepath.Join(r.dest, "qatlasd"))
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Errorf("installed mode = %v, %v", info, err)
			}
		})
	}
}

func TestInstallerDownloadAndChecksumFailures(t *testing.T) {
	for _, client := range []string{"curl", "wget"} {
		for _, fault := range []string{"404", "checksum-404", "disconnect", "timeout", "corrupt", "remote-redirect", "loop-redirect"} {
			t.Run(client+"/"+fault, func(t *testing.T) {
				want := "download failed or timed out"
				switch fault {
				case "corrupt":
					want = "checksum mismatch"
				case "remote-redirect":
					want = "redirect leaves test endpoint"
				case "loop-redirect":
					want = "too many redirects"
				}
				r := newInstallerRun(t, installerOptions{client: client, httpFault: fault})
				r.run(t, want)
			})
		}
	}
	for _, fault := range []string{"wrong", "missing", "empty", "duplicate", "conflicting", "short", "nonhex", "extra-field"} {
		t.Run("checksum/"+fault, func(t *testing.T) {
			r := newInstallerRun(t, installerOptions{checksumFault: fault})
			r.run(t, "checksum")
		})
	}
}

func TestInstallerMalformedArchives(t *testing.T) {
	for _, fault := range []string{"truncated-gzip", "not-gzip", "not-tar"} {
		t.Run(fault, func(t *testing.T) {
			r := newInstallerRun(t, installerOptions{archiveFault: fault})
			r.run(t, "cannot list archive names")
		})
	}
}

// No real HTTP client is called here. Inspect the production request contract
// and simulate a malicious HTTPS-to-HTTP redirect, without enabling test HTTP.
func TestInstallerProductionHTTPS(t *testing.T) {
	r := newInstallerRun(t, installerOptions{})
	r.setEnv("QATLAS_INSTALL_TEST_BASE_URL=")
	r.replaceTool(t, "curl", `
[ "$1" = -q ] || exit 91
headers=
proto=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --proto) proto=$2; shift ;;
        --dump-header) headers=$2; shift ;;
        -L|--location|--location-trusted|--insecure|-k) exit 92 ;;
        https://github.com/fixture/repository/releases/download/v1.2.3/qatlasd_1.2.3_checksums.txt) ;;
        http://*) echo 'attempted insecure network' >&2; exit 93 ;;
    esac
    shift
done
[ "$proto" = '=https' ] && [ -n "$headers" ] || exit 94
printf 'HTTP/1.1 302 Found\r\nLocation: http://example.invalid/never-contact\r\n\r\n' > "$headers"
`)
	r.run(t, "refusing non-HTTPS download/redirect")
	if r.requests.Load() != 0 {
		t.Fatal("production contract test must not perform HTTP")
	}
}

func TestInstallerStagedVersionBeforeRename(t *testing.T) {
	body := `#!/bin/sh
case "$0" in "$QATLAS_INSTALL_DIR"/.qatlasd-install.*/qatlasd) ;; *) exit 81 ;; esac
[ "$("$QATLAS_INSTALL_DIR/qatlasd")" = 'old binary' ] || exit 82
printf 'qatlasd version 1.2.3\n'
`
	r := newInstallerRun(t, installerOptions{entries: []installerEntry{{name: "qatlasd", body: body}}})
	r.run(t, "")
}

func TestInstallerUnsafeArchives(t *testing.T) {
	binary := installerEntry{name: "qatlasd", body: installerProgram("1.2.3")}
	cases := map[string][]installerEntry{
		"missing-binary":   {{name: "README.md", body: "docs"}},
		"duplicate-binary": {binary, binary},
		"duplicate-doc":    {binary, {name: "README.md"}, {name: "README.md"}},
		"directory-alias":  {binary, {name: "docs", kind: tar.TypeDir}, {name: "docs/", kind: tar.TypeDir}},
		"binary-symlink":   {{name: "qatlasd", kind: tar.TypeSymlink, link: "other"}},
		"binary-hardlink":  {{name: "qatlasd", kind: tar.TypeLink, link: "other"}},
		"other-symlink":    {binary, {name: "docs", kind: tar.TypeSymlink, link: "../../home"}},
		"other-hardlink":   {binary, {name: "README", kind: tar.TypeLink, link: "qatlasd"}},
		"fifo":             {binary, {name: "pipe", kind: tar.TypeFifo}},
		"device":           {binary, {name: "device", kind: tar.TypeChar}},
		"binary-directory": {{name: "qatlasd/", kind: tar.TypeDir}},
		"nested-binary":    {binary, {name: "docs/qatlasd", body: binary.body}},
		"empty":            {},
	}
	for _, path := range []string{"../escape", "docs/../../escape", "/absolute", "./README", "docs//README", "docs/./README", "docs\\escape", "new\nline", "with space", "-option"} {
		cases["path/"+path] = []installerEntry{binary, {name: path, body: "must not extract"}}
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			r := newInstallerRun(t, installerOptions{entries: entries})
			r.run(t, "unsafe archive")
		})
	}
}

func TestInstallerCandidateAndFilesystemFailures(t *testing.T) {
	for name, body := range map[string]string{
		"wrong":       installerProgram("1.2.30"),
		"dev":         installerProgram("dev"),
		"v-prefix":    installerProgram("v1.2.3"),
		"nul-suffix":  "#!/bin/sh\nprintf 'qatlasd version 1.2.3\\000hidden\\n'\n",
		"no-newline":  "#!/bin/sh\nprintf 'qatlasd version 1.2.3'\n",
		"extra-line":  installerProgram("1.2.3") + "printf '\\n'\n",
		"nonzero":     "#!/bin/sh\nexit 42\n",
		"interpreter": "#!/no/such/interpreter\n",
		"timeout":     "#!/bin/sh\nexec sleep 30\n",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			want := "version does not exactly match"
			if name == "nonzero" || name == "interpreter" || name == "timeout" {
				want = "--version failed or timed out"
			} else if name == "empty" {
				want = "binary is empty"
			}
			r := newInstallerRun(t, installerOptions{entries: []installerEntry{{name: "qatlasd", body: body}}})
			r.run(t, want)
		})
	}
	for fault, want := range map[string]string{
		"rename": "atomic replacement failed", "chmod": "cannot make staged binary executable",
		"noexec": "--version failed or timed out", "extract": "binary extraction failed",
		"destination-directory": "destination is not an ordinary file", "destination-symlink": "destination is not an ordinary file",
	} {
		t.Run(fault, func(t *testing.T) {
			r := newInstallerRun(t, installerOptions{toolFault: fault})
			r.run(t, want)
		})
	}
	t.Run("readonly", func(t *testing.T) {
		r := newInstallerRun(t, installerOptions{readonly: true})
		r.run(t, "cannot stage binary in install directory")
	})
	t.Run("fresh-failure", func(t *testing.T) {
		r := newInstallerRun(t, installerOptions{fresh: true, checksumFault: "wrong"})
		r.run(t, "checksum mismatch")
	})
}

func TestInstallerInputGuards(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		options    installerOptions
	}{
		{"intel-mac", "unsupported platform", installerOptions{platform: "darwin/amd64"}},
		{"windows", "unsupported OS", installerOptions{platform: "Windows/amd64"}},
		{"386", "unsupported architecture", installerOptions{platform: "linux/386"}},
		{"flag-missing", "requires a value", installerOptions{args: []string{"--version"}}},
		{"dir-missing", "requires a value", installerOptions{args: []string{"--dir"}}},
		{"unknown-flag", "unknown argument", installerOptions{args: []string{"--sudo"}}},
		{"version-dev", "invalid release version", installerOptions{args: []string{"--version", "dev"}}},
		{"version-path", "invalid release version", installerOptions{args: []string{"--version", "../v1.2.3"}}},
		{"version-newline", "invalid release version", installerOptions{args: []string{"--version", "1.2.3\n../../escape"}}},
		{"repo-path", "invalid GitHub", installerOptions{env: []string{"QATLAS_REPO=fixture/../repository"}}},
		{"repo-newline", "invalid GitHub", installerOptions{env: []string{"QATLAS_REPO=fixture/repository\nevil"}}},
		{"timeout-zero", "timeouts must", installerOptions{env: []string{"QATLAS_INSTALL_TIMEOUT=0"}}},
		{"timeout-huge", "timeouts must", installerOptions{env: []string{"QATLAS_INSTALL_TIMEOUT=999999999999999999"}}},
		{"remote-http", "test endpoint must", installerOptions{env: []string{"QATLAS_INSTALL_TEST_BASE_URL=http://example.invalid:80"}}},
		{"loopback-userinfo", "test endpoint must", installerOptions{env: []string{"QATLAS_INSTALL_TEST_BASE_URL=http://127.0.0.1:80@example.invalid"}}},
		{"loopback-path", "test endpoint must", installerOptions{env: []string{"QATLAS_INSTALL_TEST_BASE_URL=http://127.0.0.1:80/"}}},
		{"latest-unresolved", "could not resolve latest", installerOptions{httpFault: "latest-unresolved", env: []string{"QATLAS_VERSION=latest"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newInstallerRun(t, tc.options)
			r.run(t, tc.want)
			if tc.name != "latest-unresolved" && r.requests.Load() != 0 {
				t.Errorf("invalid input made %d HTTP requests", r.requests.Load())
			}
		})
	}
}
