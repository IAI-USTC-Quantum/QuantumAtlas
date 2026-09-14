package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const smokeTimeout = 15 * time.Second
const smokeBodyLimit = 8 << 20

// All helpers are test-only and standard-library-only. No environment or auth
// files are read here. In particular, configuration is NOT parsed in init or
// TestMain: tagged fixture-only runs must remain independent of live secrets.
type smokeTarget struct {
	base     *url.URL
	insecure bool
	token    string
}

// parseSmokeTargets accepts comma/newline-separated URL[|insecure][|token=...]
// or URL[|insecure][|token-env=...]. Multiple token sources and duplicate flags
// are rejected rather than silently overriding credentials. Errors NEVER quote
// input, URLs, flag values, environment variable names, or underlying errors.
func parseSmokeTargets(raw string, lookup func(string) (string, bool)) ([]smokeTarget, error) {
	var targets []smokeTarget
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		bad := func(reason string) ([]smokeTarget, error) {
			return nil, fmt.Errorf("QATLAS_SERVER_TARGETS target-%d: %s", len(targets)+1, reason)
		}
		parts := strings.Split(entry, "|")
		base, err := url.Parse(strings.TrimSpace(parts[0]))
		if err != nil || strings.Contains(parts[0], "#") || !safeSmokeURL(base) {
			return bad("expected an http(s) URL without userinfo, query, or fragment")
		}
		if port := base.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return bad("invalid URL port")
			}
		}
		target := smokeTarget{base: base}
		hasToken := false
		for _, flag := range parts[1:] {
			flag = strings.TrimSpace(flag)
			switch {
			case flag == "insecure":
				if target.insecure {
					return bad("duplicate insecure flag")
				}
				target.insecure = true
			case strings.HasPrefix(flag, "token=") || strings.HasPrefix(flag, "token-env="):
				if hasToken {
					return bad("only one token source is allowed")
				}
				hasToken = true
				if strings.HasPrefix(flag, "token-env=") {
					name := strings.TrimSpace(strings.TrimPrefix(flag, "token-env="))
					if !smokeEnvName.MatchString(name) || lookup == nil {
						return bad("token-env requires a valid environment variable name")
					}
					value, found := lookup(name)
					if !found || strings.TrimSpace(value) == "" {
						return bad("token-env variable is unset or empty")
					}
					target.token = strings.TrimSpace(value)
				} else {
					target.token = strings.TrimSpace(strings.TrimPrefix(flag, "token="))
				}
				if !validSmokeToken(target.token) {
					return bad("token must be nonempty printable ASCII without whitespace or delimiters")
				}
			default:
				return bad("unknown or empty flag")
			}
		}
		if target.token != "" && (strings.Contains(base.String(), target.token) || strings.Contains(base.Path, target.token)) {
			return bad("credential must not occur in the URL")
		}
		// Treat a target path as a deployment prefix, not as a filename.
		base.Path = strings.TrimRight(base.Path, "/") + "/"
		if base.RawPath != "" {
			base.RawPath = strings.TrimRight(base.RawPath, "/") + "/"
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, errors.New("QATLAS_SERVER_TARGETS is required: configure at least one URL (live smoke was explicitly selected)")
	}
	return targets, nil
}

var smokeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validSmokeToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r <= ' ' || r > '~' || r == ',' || r == '|' {
			return false
		}
	}
	return true
}

func safeSmokeURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.Opaque == "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		u.Fragment == "" && u.RawFragment == "" && !strings.Contains(u.String(), "#") &&
		!strings.ContainsAny(u.Host+u.Path, "\\\r\n\t")
}

func sameSmokeOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func newSmokeClient(target smokeTarget, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 || timeout > smokeTimeout {
		return nil, errors.New("smoke timeout must be positive and at most 15 seconds")
	}
	transport := &http.Transport{
		// No proxy/environment auth or cookie jar is inherited by these probes.
		DialContext:            (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: target.insecure}, // Only explicit |insecure opts out.
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		IdleConnTimeout:        timeout,
		MaxResponseHeaderBytes: 1 << 20,
		DisableKeepAlives:      true, // Also avoids unsolicited idle-response logging of untrusted bytes.
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Reject ALL redirects, even same-origin: don't send a credential to
		// an unexpected path, and don't turn login redirects into green checks.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

type smokeResponse struct {
	status    int
	mediaType string
	body      []byte
}

// smokeGet only allows GET. Credentials can go to the two explicitly checked
// API paths, never to the SPA or its resources. Network/JSON errors and server
// responses are untrusted and may echo secrets; never wrap or dump them.
func smokeGet(client *http.Client, target smokeTarget, reference string, authenticated bool) (smokeResponse, error) {
	var out smokeResponse
	ref, err := url.Parse(reference)
	if err != nil || strings.Contains(reference, "#") {
		return out, errors.New("invalid request reference")
	}
	u := target.base.ResolveReference(ref)
	if !safeSmokeURL(u) || !sameSmokeOrigin(target.base, u) {
		return out, errors.New("request URL violates same-origin safety boundary")
	}
	if target.token != "" && (strings.Contains(u.String(), target.token) || strings.Contains(u.Path, target.token)) {
		return out, errors.New("request URL contains a credential")
	}
	if authenticated && (target.token == "" || (reference != "api/health" && reference != "api/papers/stats")) {
		return out, errors.New("authenticated request outside allowed API paths")
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return out, errors.New("could not construct request")
	}
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+target.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		// In particular, url.Error includes the request URL and transport
		// errors may include response text. Neither is safe to print.
		return out, errors.New("HTTP request failed (transport, TLS, or timeout); response and URL withheld")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return out, errors.New("HTTP redirect refused; Location withheld")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, smokeBodyLimit+1))
	if err != nil {
		return out, errors.New("HTTP response body could not be read")
	}
	if len(body) > smokeBodyLimit {
		return out, errors.New("HTTP response exceeds smoke body limit")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return out, errors.New("HTTP response has missing or invalid Content-Type")
	}
	return smokeResponse{status: resp.StatusCode, mediaType: mediaType, body: body}, nil
}

func smokeObject(response smokeResponse, status int) (map[string]json.RawMessage, error) {
	if response.status != status {
		return nil, fmt.Errorf("unexpected HTTP status %d (want %d); body withheld", response.status, status)
	}
	if response.mediaType != "application/json" {
		return nil, errors.New("expected application/json response; body withheld")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(response.body, &object); err != nil || object == nil {
		return nil, errors.New("expected one JSON object; body withheld")
	}
	return object, nil
}

func smokeVersion(raw json.RawMessage, expected string) error {
	var version string
	if json.Unmarshal(raw, &version) != nil || strings.TrimSpace(version) == "" || version != strings.TrimSpace(version) || strings.EqualFold(version, "dev") {
		return errors.New("version must be nonempty and non-dev")
	}
	if expected != "" && version != expected {
		return errors.New("version differs from QATLAS_EXPECTED_VERSION; values withheld")
	}
	return nil
}

func checkSmokeHealth(response smokeResponse, authenticated bool, expected string) error {
	body, err := smokeObject(response, http.StatusOK)
	if err != nil {
		return err
	}
	var code int
	var message string
	var data map[string]json.RawMessage
	if json.Unmarshal(body["code"], &code) != nil || code != 200 || json.Unmarshal(body["message"], &message) != nil || message == "" || json.Unmarshal(body["data"], &data) != nil || data == nil {
		return errors.New("invalid health envelope")
	}
	if !authenticated {
		if message != "API is healthy." && message != "Dependency degraded." {
			return errors.New("anonymous health message is not a public health summary")
		}
		for key := range body {
			if key != "code" && key != "message" && key != "data" {
				return errors.New("anonymous health envelope leaked an unexpected field")
			}
		}
		for key := range data {
			switch key {
			case "status", "version", "uptime_seconds", "time", "checks":
			default:
				return errors.New("anonymous health data leaked a detail field")
			}
		}
	}
	var checks map[string]map[string]json.RawMessage
	if json.Unmarshal(data["checks"], &checks) != nil || len(checks) == 0 {
		return errors.New("health checks missing or invalid")
	}
	// Enforce privacy BEFORE liveness, including on degraded/error responses.
	if !authenticated {
		for _, check := range checks {
			for key := range check {
				if key != "status" {
					return errors.New("anonymous health check leaked a detail field")
				}
			}
		}
	}
	for _, name := range []string{"rawstore", "postgres", "registry"} {
		if checks[name] == nil {
			return errors.New("health is missing a current dependency check")
		}
	}
	for name, check := range checks {
		var status string
		if json.Unmarshal(check["status"], &status) != nil || (status != "ok" && status != "not_configured") || (name == "rawstore" && status != "ok") {
			return errors.New("health dependency is unhealthy or has an invalid status")
		}
	}
	var status, timestamp string
	var uptime *int64
	if json.Unmarshal(data["status"], &status) != nil || status != "healthy" {
		return errors.New("aggregate health is not healthy")
	}
	if json.Unmarshal(data["uptime_seconds"], &uptime) != nil || uptime == nil || *uptime < 0 || json.Unmarshal(data["time"], &timestamp) != nil {
		return errors.New("invalid health uptime or timestamp")
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		return errors.New("invalid health timestamp")
	}
	if err := smokeVersion(data["version"], expected); err != nil {
		return err
	}
	if authenticated {
		// Every healthy rawstore, including local storage, has a backend.
		// Latency/schema_version can legitimately be omitted when zero;
		// postgres/registry may legitimately be not_configured.
		var backend string
		if json.Unmarshal(checks["rawstore"]["backend"], &backend) != nil || (backend != "local" && backend != "s3" && backend != "s3-router") {
			return errors.New("authenticated health lacks rawstore backend detail (requires system PAT or session JWT, not a user PAT)")
		}
	}
	return nil
}

func checkSmokeInfo(response smokeResponse, expected string) error {
	body, err := smokeObject(response, http.StatusOK)
	if err != nil {
		return err
	}
	var mode, engine string
	if json.Unmarshal(body["mode"], &mode) != nil || mode != "server" || json.Unmarshal(body["engine"], &engine) != nil || engine != "go+pocketbase" {
		return errors.New("server info does not identify the Go server")
	}
	return smokeVersion(body["version"], expected)
}

func checkSmokeProtected(response smokeResponse, authenticated bool) error {
	want := http.StatusUnauthorized
	if authenticated {
		want = http.StatusOK
	}
	body, err := smokeObject(response, want)
	if err != nil {
		return err
	}
	if authenticated {
		// paperStatsHandler explicitly supports available:false with an
		// unconfigured registry. Don't invent mandatory corpus counts.
		var available *bool
		if json.Unmarshal(body["available"], &available) != nil || available == nil {
			return errors.New("paper stats is missing its available boolean")
		}
		return nil
	}
	// Verified against current internal/routes/auth.go, not the retired
	// /api/stats endpoint or an assumed generic error envelope.
	var detail string
	if json.Unmarshal(body["detail"], &detail) != nil || !strings.Contains(strings.ToLower(detail), "authentication required") {
		return errors.New("protected read lacks the current authentication-required response")
	}
	return nil
}

// These narrowly scoped patterns recognize the bundled Vite shell, not general
// HTML. A base tag is rejected rather than guessing browser URL resolution.
var smokeScriptTag = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
var smokeScriptSrc = regexp.MustCompile(`(?is)(?:^|\s)src\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
var smokeBaseTag = regexp.MustCompile(`(?i)<base\b`)
var smokeTitle = regexp.MustCompile(`(?is)<title>\s*QuantumAtlas\s*</title>`)
var smokeRoot = regexp.MustCompile(`(?i)\bid\s*=\s*(?:"root"|'root')`)

func smokeSPAAssets(response smokeResponse, target smokeTarget) ([]string, error) {
	if response.status != http.StatusOK || response.mediaType != "text/html" {
		return nil, errors.New("SPA must return HTTP 200 text/html")
	}
	if !smokeTitle.Match(response.body) || !smokeRoot.Match(response.body) || smokeBaseTag.Match(response.body) {
		return nil, errors.New("SPA shell markers missing or unsupported base tag present")
	}
	var assets []string
	seen := make(map[string]bool)
	for _, tag := range smokeScriptTag.FindAllSubmatch(response.body, -1) {
		match := smokeScriptSrc.FindSubmatch(tag[1])
		if match == nil {
			continue
		}
		source := ""
		for _, value := range match[1:] {
			if len(value) != 0 {
				source = html.UnescapeString(string(value))
				break
			}
		}
		ref, err := url.Parse(source)
		if err != nil || source == "" || strings.Contains(source, "#") {
			return nil, errors.New("SPA script reference is invalid")
		}
		u := target.base.ResolveReference(ref)
		if !safeSmokeURL(u) || !sameSmokeOrigin(target.base, u) {
			return nil, errors.New("SPA script violates same-origin safety boundary")
		}
		if target.token != "" && (strings.Contains(u.String(), target.token) || strings.Contains(u.Path, target.token)) {
			return nil, errors.New("SPA script URL contains a credential")
		}
		if !strings.HasSuffix(u.Path, ".js") || !(strings.HasPrefix(u.Path, "/assets/") || strings.HasPrefix(u.Path, target.base.Path+"assets/")) {
			return nil, errors.New("SPA script is not a bundled JavaScript asset")
		}
		if !seen[u.String()] {
			seen[u.String()] = true
			assets = append(assets, u.String())
		}
	}
	if len(assets) == 0 || len(assets) > 16 {
		return nil, errors.New("SPA must reference between 1 and 16 bundled JavaScript assets")
	}
	return assets, nil
}

func checkSmokeJavaScript(response smokeResponse) error {
	if response.status != http.StatusOK {
		return errors.New("SPA JavaScript asset did not return HTTP 200")
	}
	if response.mediaType != "text/javascript" && response.mediaType != "application/javascript" {
		return errors.New("SPA asset has a non-JavaScript MIME type")
	}
	body := bytes.TrimSpace(response.body)
	if len(body) == 0 || bytes.HasPrefix(body, []byte("<")) {
		return errors.New("SPA JavaScript asset is empty or appears to be HTML")
	}
	return nil
}

func checkSmokeSPA(client *http.Client, target smokeTarget) error {
	index, err := smokeGet(client, target, "./", false)
	if err != nil {
		return err
	}
	// Validate ALL references before fetching any of them; none carry auth.
	assets, err := smokeSPAAssets(index, target)
	if err != nil {
		return err
	}
	for _, asset := range assets {
		response, err := smokeGet(client, target, asset, false)
		if err != nil {
			return err
		}
		if err := checkSmokeJavaScript(response); err != nil {
			return err
		}
	}
	return nil
}
