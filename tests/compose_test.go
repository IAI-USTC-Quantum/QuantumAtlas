package tests

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These are source-level deployment contracts, not a replacement for image smoke tests.
var composeServices = []struct {
	name, profile, version string
}{
	{"qatlasd", "", "QATLAS_VERSION"},
	{"qatlas-search", "search", "QATLAS_SEARCH_VERSION"},
	{"qatlas-match", "match", "QATLAS_MATCH_VERSION"},
	{"qatlas-rag", "rag", "QATLAS_RAG_VERSION"},
}

type composeService struct {
	Image       string               `yaml:"image"`
	Profiles    []string             `yaml:"profiles"`
	Ports       []yaml.Node          `yaml:"ports"`
	Volumes     []yaml.Node          `yaml:"volumes"`
	ExtraHosts  yaml.Node            `yaml:"extra_hosts"`
	NetworkMode string               `yaml:"network_mode"`
	Other       map[string]yaml.Node `yaml:",inline"`
}

// Prefer the source location; walking upward from cwd also works with -trimpath
// and with go test invoked from either the repository root or tests directory.
func composeRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	starts := []string{cwd}
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		starts = append([]string{filepath.Dir(file)}, starts...)
	}
	for _, start := range starts {
		for dir := start; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				if _, err := os.Stat(filepath.Join(dir, "deploy", "docker-compose.yml")); err == nil {
					return dir
				}
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	t.Fatal("cannot locate QuantumAtlas repository root (go.mod)")
	return ""
}

func composeRead(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestComposeDeployment(t *testing.T) {
	root := composeRoot(t)
	for _, tc := range []struct {
		file       string
		standalone bool
	}{
		{"docker-compose.yml", false},
		{"docker-compose.standalone.yml", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var doc struct {
				Services map[string]composeService `yaml:"services"`
			}
			if err := yaml.Unmarshal(composeRead(t, root, "deploy/"+tc.file), &doc); err != nil {
				t.Fatalf("parse compose YAML: %v", err)
			}
			want := composeServices
			if tc.standalone {
				want = want[:1]
			}
			if len(doc.Services) != len(want) {
				t.Errorf("service count = %d, want %d; databases/object storage must remain external", len(doc.Services), len(want))
			}
			for _, expected := range want {
				t.Run(expected.name, func(t *testing.T) {
					svc, ok := doc.Services[expected.name]
					if !ok {
						t.Fatalf("required service %q is missing", expected.name)
					}
					// Tags may be pinned, defaulted or required; do not force "latest".
					imagePattern := "^" + regexp.QuoteMeta("ghcr.io/iai-ustc-quantum/"+expected.name+":${"+expected.version) + `(?::?[-?][^}]*)?\}$`
					if !regexp.MustCompile(imagePattern).MatchString(svc.Image) {
						t.Errorf("image = %q; want release image from GHCR tagged via ${%s}", svc.Image, expected.version)
					}
					if _, ok := svc.Other["build"]; ok {
						t.Error("build must be absent: deployment hosts pull release images")
					}
					if svc.NetworkMode == "host" {
						t.Error("network_mode: host bypasses the port isolation contract")
					}
					if expected.profile != "" {
						if !slices.Contains(svc.Profiles, expected.profile) {
							t.Errorf("profiles = %v; optional service needs profile %q", svc.Profiles, expected.profile)
						}
						if len(svc.Ports) != 0 {
							t.Error("optional service must not publish host ports")
						}
						// Search/RAG admin endpoints persist config; only match is read-only.
						composeRequireMount(t, svc, "${HOME}/.qatlas/"+expected.profile+".yaml", "/etc/"+expected.name+"/config.yaml", expected.profile == "match")
						return
					}
					if len(svc.Profiles) != 0 {
						t.Errorf("qatlasd must start by default, got profiles %v", svc.Profiles)
					}
					if user, ok := svc.Other["user"]; ok {
						if uid := strings.SplitN(user.Value, ":", 2)[0]; uid != "nonroot" && uid != "65532" {
							t.Errorf("user = %q; must not override the image's nonroot/65532 runtime identity", user.Value)
						}
					}
					for _, key := range []string{"environment", "env_file", "depends_on"} {
						if _, ok := svc.Other[key]; ok {
							t.Errorf("%s must be absent: app config is YAML and backing services are external", key)
						}
					}
					for _, mount := range []struct {
						source, target string
						readOnly       bool
					}{
						{"${HOME}/.qatlas/config.yaml", "/home/nonroot/.qatlas/config.yaml", true},
						{"${HOME}/.qatlas/docs", "/home/nonroot/.qatlas/docs", true},
						{"", "/data/raw", false},
						{"", "/data/pb_data", false},
					} {
						composeRequireMount(t, svc, mount.source, mount.target, mount.readOnly)
					}
					if len(svc.Ports) == 0 {
						t.Error("qatlasd needs a loopback host port for the reverse proxy")
					}
					for i, port := range svc.Ports {
						var host string
						if port.Kind == yaml.ScalarNode {
							// Strip the container port, then parse host:published (IPv4 or IPv6).
							if j := strings.LastIndex(port.Value, ":"); j >= 0 {
								host, _, _ = net.SplitHostPort(port.Value[:j])
							}
						} else {
							var binding struct {
								HostIP string `yaml:"host_ip"`
							}
							if err := port.Decode(&binding); err != nil {
								t.Fatalf("ports[%d]: %v", i, err)
							}
							host = binding.HostIP
						}
						if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
							t.Errorf("ports[%d] host IP = %q; EVERY published port must bind loopback", i, host)
						}
					}
					if !tc.standalone {
						composeRequireHostGateway(t, svc.ExtraHosts)
					}
				})
			}
		})
	}
}

// Accept Compose's short and long mount syntax without tying tests to YAML style.
func composeRequireMount(t *testing.T, svc composeService, source, target string, readOnly bool) {
	t.Helper()
	found := 0
	for i, node := range svc.Volumes {
		var mount struct {
			Type     string `yaml:"type"`
			Source   string `yaml:"source"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
		}
		if node.Kind == yaml.ScalarNode {
			parts := strings.Split(node.Value, ":")
			if len(parts) < 2 || len(parts) > 3 {
				t.Fatalf("volumes[%d]: unsupported mount %q", i, node.Value)
			}
			mount.Source, mount.Target = parts[0], parts[1]
			if len(parts) == 3 {
				mount.ReadOnly = slices.Contains(strings.Split(parts[2], ","), "ro")
			}
		} else if err := node.Decode(&mount); err != nil {
			t.Fatalf("volumes[%d]: %v", i, err)
		}
		if mount.Target != target {
			continue
		}
		found++
		if mount.Source == "" || (source != "" && mount.Source != source) {
			t.Errorf("mount %s source = %q; want %q (nonempty persistent source)", target, mount.Source, source)
		}
		if source != "" && node.Kind != yaml.ScalarNode && mount.Type != "bind" {
			t.Errorf("mount %s type = %q; config/docs must be host bind mounts", target, mount.Type)
		}
		if mount.ReadOnly != readOnly {
			t.Errorf("mount %s read_only = %t, want %t", target, mount.ReadOnly, readOnly)
		}
	}
	if found != 1 {
		t.Errorf("mount target %s occurs %d times, want exactly once", target, found)
	}
}

func composeRequireHostGateway(t *testing.T, node yaml.Node) {
	t.Helper()
	hosts := map[string]string{}
	if node.Kind == yaml.MappingNode {
		if err := node.Decode(&hosts); err != nil {
			t.Fatalf("extra_hosts: %v", err)
		}
	} else {
		var entries []string
		if err := node.Decode(&entries); err != nil {
			t.Fatalf("extra_hosts: %v", err)
		}
		for _, entry := range entries {
			if i := strings.IndexAny(entry, ":="); i >= 0 {
				hosts[entry[:i]] = entry[i+1:]
			}
		}
	}
	if hosts["host.docker.internal"] != "host-gateway" {
		t.Errorf("extra_hosts must map host.docker.internal to host-gateway for external PostgreSQL; got %v", hosts)
	}
}

func TestComposeEnvExample(t *testing.T) {
	// Read only the committed example; never load deploy/.env or process env.
	example := composeRead(t, composeRoot(t), "deploy/.env.docker.example")
	allowed := map[string]bool{}
	for _, svc := range composeServices {
		allowed[svc.version] = false
	}
	for i, line := range strings.Split(string(example), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if _, known := allowed[key]; !ok || !known {
			t.Errorf(".env.docker.example:%d: only image-version assignments are allowed, got %q", i+1, key)
			continue
		}
		if allowed[key] || strings.TrimSpace(value) == "" {
			t.Errorf(".env.docker.example:%d: %s must have one nonempty assignment", i+1, key)
		}
		allowed[key] = true
	}
	for key, seen := range allowed {
		if !seen {
			t.Errorf(".env.docker.example is missing image-version assignment %s", key)
		}
	}
}

func TestComposeDockerfileRuntime(t *testing.T) {
	// Inspect actual instructions in the final stage, not comments, stage names,
	// a fixed Debian release or incidental compiler flags. Linking needs a real
	// image smoke test; -extldflags=-static is not required for CGO_ENABLED=0.
	data := composeRead(t, composeRoot(t), "Dockerfile")
	stages := 0
	base, user, logical := "", "", ""
	volumes := []string{}
	copiesArtifact := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		logical += strings.TrimSuffix(line, "\\") + " "
		if strings.HasSuffix(line, "\\") {
			continue
		}
		fields := strings.Fields(logical)
		args := strings.TrimSpace(logical[len(fields[0]):])
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			stages++
			base, user, volumes, copiesArtifact = "", "", nil, false
			for _, field := range fields[1:] {
				if !strings.HasPrefix(field, "--") {
					base = field
					break
				}
			}
		case "USER":
			user = args
		case "COPY":
			copiesArtifact = copiesArtifact || strings.Contains(args, "--from=")
		case "VOLUME":
			paths := strings.Fields(args)
			if strings.HasPrefix(args, "[") {
				if err := json.Unmarshal([]byte(args), &paths); err != nil {
					t.Fatalf("Dockerfile VOLUME %s: %v", args, err)
				}
			}
			for _, path := range paths {
				volumes = append(volumes, strings.Trim(path, `"'`))
			}
		}
		logical = ""
	}
	if stages < 2 || !copiesArtifact {
		t.Error("Dockerfile must use multiple stages and COPY --from to transfer the runtime artifact")
	}
	if !regexp.MustCompile(`^gcr\.io/distroless/static(?:-[^:@]+)?[:@]`).MatchString(base) {
		t.Errorf("final FROM = %q; want a versioned distroless/static runtime image", base)
	}
	// The known nonroot image supplies UID 65532 when USER is omitted.
	if user == "" && regexp.MustCompile(`:nonroot(?:-[^@]+)?(?:@|$)`).MatchString(base) {
		user = "nonroot"
	}
	if uid := strings.SplitN(user, ":", 2)[0]; uid != "nonroot" && uid != "65532" {
		t.Errorf("final runtime USER = %q; want nonroot/65532 to match host volume ownership", user)
	}
	for _, path := range []string{"/data/raw", "/data/pb_data"} {
		if !slices.Contains(volumes, path) {
			t.Errorf("final-stage VOLUME is missing %s; got %v", path, volumes)
		}
	}
	if slices.Contains(volumes, "/data/wiki") {
		t.Error("obsolete /data/wiki must not be a runtime volume")
	}
}
