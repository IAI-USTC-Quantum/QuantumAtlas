package e2e

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureSecret = "fixture-only-secret-DO-NOT-PRINT"
const fixtureHealth = `{"code":200,"message":"API is healthy.","data":{"status":"healthy","version":"v1.2.3","uptime_seconds":10,"time":"2026-04-01T00:00:00Z","checks":{"rawstore":{"status":"ok"},"postgres":{"status":"not_configured"},"registry":{"status":"not_configured"}}}}`
const fixtureInfo = `{"mode":"server","engine":"go+pocketbase","version":"v1.2.3","capabilities":{}}`

// All URLs used for requests below come from httptest. Configuration parser
// examples use reserved .invalid names but never resolve them. lookup is always
// an in-memory function, never os.Getenv/LookupEnv or a deployment auth file.
func fixtureTarget(t *testing.T, spec string) smokeTarget {
	t.Helper()
	targets, err := parseSmokeTargets(spec, func(string) (string, bool) { return "", false })
	if err != nil || len(targets) != 1 {
		t.Fatal("could not construct offline fixture target")
	}
	return targets[0]
}

func fixtureClient(t *testing.T, target smokeTarget, timeout time.Duration) *http.Client {
	t.Helper()
	client, err := newSmokeClient(target, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func fixtureHTTPResponse(t *testing.T, status int, contentType, body string) smokeResponse {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("offline response fixture received unexpected method or credentials")
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	target := fixtureTarget(t, server.URL)
	response, err := smokeGet(fixtureClient(t, target, smokeTimeout), target, "api/health", false)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func requireSecretSafeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), fixtureSecret) || strings.Contains(err.Error(), "edge.invalid") {
		t.Fatal("error disclosed confidential fixture input")
	}
}

func TestSmokeFixtureParseTargets(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "FIXTURE_TOKEN" {
			return fixtureSecret, true
		}
		return "", false
	}
	targets, err := parseSmokeTargets(" https://first.invalid/ ,\nhttps://second.invalid/prefix|insecure|token-env=FIXTURE_TOKEN\r\nhttp://third.invalid|token="+fixtureSecret, lookup)
	if err != nil || len(targets) != 3 {
		t.Fatal("valid CSV/newline configuration rejected")
	}
	if targets[0].insecure || targets[0].token != "" || targets[0].base.Path != "/" {
		t.Fatal("anonymous verified-TLS defaults changed")
	}
	if !targets[1].insecure || targets[1].token != fixtureSecret || targets[1].base.Path != "/prefix/" || targets[2].token != fixtureSecret {
		t.Fatal("explicit flags or deployment prefix parsed incorrectly")
	}
	cases := []struct{ name, raw string }{
		{"absent", ""}, {"blank", " ,\r\n , "},
		{"missing_scheme", "edge.invalid|token=" + fixtureSecret},
		{"wrong_scheme", "ftp://edge.invalid|token=" + fixtureSecret},
		{"missing_host", "https:///path|token=" + fixtureSecret},
		{"bad_escape", "https://edge.invalid/%xx|token=" + fixtureSecret},
		{"userinfo", "https://user:" + fixtureSecret + "@edge.invalid"},
		{"query", "https://edge.invalid?token=" + fixtureSecret},
		{"empty_query", "https://edge.invalid?"},
		{"fragment", "https://edge.invalid#" + fixtureSecret},
		{"empty_fragment", "https://edge.invalid#"},
		{"invalid_port", "https://edge.invalid:bad|token=" + fixtureSecret},
		{"port_range", "https://edge.invalid:65536"},
		{"empty_token", "https://edge.invalid|token="},
		{"missing_env", "https://edge.invalid|token-env=UNSET"},
		{"invalid_env_name", "https://edge.invalid|token-env=" + fixtureSecret},
		{"empty_flag", "https://edge.invalid|"},
		{"unknown_flag", "https://edge.invalid|" + fixtureSecret},
		{"duplicate_tls", "https://edge.invalid|insecure|insecure"},
		{"duplicate_token", "https://edge.invalid|token=" + fixtureSecret + "|token=other"},
		{"ambiguous_token", "https://edge.invalid|token-env=FIXTURE_TOKEN|token=" + fixtureSecret},
		{"bad_header", "https://edge.invalid|token=two\twords"},
		{"token_in_path", "https://edge.invalid/" + fixtureSecret + "|token=" + fixtureSecret},
		{"later_bad_target", "https://first.invalid,https://edge.invalid|" + fixtureSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSmokeTargets(tc.raw, lookup)
			requireSecretSafeError(t, err)
			if len(got) != 0 {
				t.Fatal("bad configuration returned partially usable targets")
			}
		})
	}
	t.Run("invalid_env_token", func(t *testing.T) {
		_, err := parseSmokeTargets("https://edge.invalid|token-env=FIXTURE_TOKEN", func(string) (string, bool) { return fixtureSecret + "\r\nInjected: value", true })
		requireSecretSafeError(t, err)
	})
}

func TestSmokeFixtureHealthContract(t *testing.T) {
	detailed := strings.Replace(fixtureHealth, `"rawstore":{"status":"ok"}`, `"rawstore":{"status":"ok","backend":"local"}`, 1)
	cases := []struct {
		name, body, expected string
		auth, wantError      bool
		status               int
	}{
		{"anonymous_optional_dependencies", fixtureHealth, "", false, false, 200},
		{"anonymous_exact_version", fixtureHealth, "v1.2.3", false, false, 200},
		{"authenticated_local_backend", detailed, "", true, false, 200},
		{"auth_sanitised", fixtureHealth, "", true, true, 200},
		{"anon_backend_leak", detailed, "", false, true, 200},
		{"mineru_leak", strings.Replace(fixtureHealth, `"checks":`, `"mineru":{"secret":"`+fixtureSecret+`"},"checks":`, 1), "", false, true, 200},
		{"schema_leak", strings.Replace(fixtureHealth, `"registry":{"status":"not_configured"}`, `"registry":{"status":"ok","schema_version":7}`, 1), "", false, true, 200},
		{"unknown_check_field", strings.Replace(fixtureHealth, `"rawstore":{"status":"ok"}`, `"rawstore":{"status":"ok","`+fixtureSecret+`":"hidden"}`, 1), "", false, true, 200},
		{"degraded", strings.Replace(fixtureHealth, `"status":"healthy"`, `"status":"degraded"`, 1), "", false, true, 200},
		{"dependency_error", strings.Replace(fixtureHealth, `"postgres":{"status":"not_configured"}`, `"postgres":{"status":"error"}`, 1), "", false, true, 200},
		{"unknown_status", strings.Replace(fixtureHealth, `"status":"ok"`, `"status":"`+fixtureSecret+`"`, 1), "", false, true, 200},
		{"rawstore_not_configured", strings.Replace(fixtureHealth, `"status":"ok"`, `"status":"not_configured"`, 1), "", false, true, 200},
		{"missing_check", strings.Replace(fixtureHealth, `,"registry":{"status":"not_configured"}`, "", 1), "", false, true, 200},
		{"empty_checks", `{"code":200,"message":"healthy","data":{"checks":{}}}`, "", false, true, 200},
		{"bad_envelope", strings.Replace(fixtureHealth, `"code":200`, `"code":503`, 1), "", false, true, 200},
		{"bad_http_status", fixtureHealth, "", false, true, 503},
		{"negative_uptime", strings.Replace(fixtureHealth, `"uptime_seconds":10`, `"uptime_seconds":-1`, 1), "", false, true, 200},
		{"null_uptime", strings.Replace(fixtureHealth, `"uptime_seconds":10`, `"uptime_seconds":null`, 1), "", false, true, 200},
		{"message_leak", strings.Replace(fixtureHealth, "API is healthy.", fixtureSecret, 1), "", false, true, 200},
		{"bad_time", strings.Replace(fixtureHealth, "2026-04-01T00:00:00Z", fixtureSecret, 1), "", false, true, 200},
		{"dev_version", strings.Replace(fixtureHealth, "v1.2.3", "dev", 1), "", false, true, 200},
		{"empty_version", strings.Replace(fixtureHealth, "v1.2.3", "", 1), "", false, true, 200},
		{"version_mismatch", fixtureHealth, fixtureSecret, false, true, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSmokeHealth(fixtureHTTPResponse(t, tc.status, "application/json; charset=utf-8", tc.body), tc.auth, tc.expected)
			if tc.wantError {
				requireSecretSafeError(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("privacy_checked_before_degradation", func(t *testing.T) {
		body := strings.Replace(fixtureHealth, `"status":"healthy"`, `"status":"degraded"`, 1)
		body = strings.Replace(body, `"postgres":{"status":"not_configured"}`, `"postgres":{"status":"error","error":"`+fixtureSecret+`"}`, 1)
		err := checkSmokeHealth(fixtureHTTPResponse(t, 200, "application/json", body), false, "")
		requireSecretSafeError(t, err)
		if !strings.Contains(err.Error(), "leaked") {
			t.Fatal("degraded state masked the privacy failure")
		}
	})
}

func TestSmokeFixtureInfoAndProtectedRead(t *testing.T) {
	for _, tc := range []struct {
		name, body, expected string
		bad                  bool
	}{
		{"info", fixtureInfo, "", false},
		{"expected_version", fixtureInfo, "v1.2.3", false},
		{"wrong_version", fixtureInfo, fixtureSecret, true},
		{"dev", strings.Replace(fixtureInfo, "v1.2.3", "dev", 1), "", true},
		{"missing_version", `{"mode":"server","engine":"go+pocketbase"}`, "", true},
		{"wrong_engine", strings.Replace(fixtureInfo, "go+pocketbase", fixtureSecret, 1), "", true},
		{"malformed", `{"` + fixtureSecret, "", true},
		{"trailing_json", fixtureInfo + fixtureInfo, "", true},
		{"null", "null", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSmokeInfo(fixtureHTTPResponse(t, 200, "application/json", tc.body), tc.expected)
			if tc.bad {
				requireSecretSafeError(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name, body string
		status     int
		auth, bad  bool
	}{
		{"anonymous_denied", `{"detail":"authentication required (sign in at /login then mint a PAT at /pat)"}`, 401, false, false},
		{"anonymous_open", `{"available":true}`, 200, false, true},
		{"anonymous_forbidden", `{"detail":"authentication required"}`, 403, false, true},
		{"wrong_error_shape", `{"error":"authentication required"}`, 401, false, true},
		{"echo_secret", `{"detail":"` + fixtureSecret + `"}`, 401, false, true},
		{"authorized_stats", `{"available":true}`, 200, true, false},
		{"optional_registry", `{"available":false}`, 200, true, false},
		{"missing_available", `{}`, 200, true, true},
		{"null_available", `{"available":null}`, 200, true, true},
		{"invalid_available", `{"available":"true"}`, 200, true, true},
		{"auth_denied", `{"detail":"authentication required"}`, 401, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSmokeProtected(fixtureHTTPResponse(t, tc.status, "application/json", tc.body), tc.auth)
			if tc.bad {
				requireSecretSafeError(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("wrong_json_mime", func(t *testing.T) {
		requireSecretSafeError(t, checkSmokeInfo(fixtureHTTPResponse(t, 200, "text/html", fixtureSecret), ""))
	})
}

func TestSmokeFixtureTLSAndTimeout(t *testing.T) {
	t.Run("tls_default_and_explicit_opt_out", func(t *testing.T) {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fixtureInfo)
		}))
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.StartTLS()
		defer server.Close()
		verified := fixtureTarget(t, server.URL)
		_, err := smokeGet(fixtureClient(t, verified, smokeTimeout), verified, "api/server/info", false)
		requireSecretSafeError(t, err)
		insecure := fixtureTarget(t, server.URL+"|insecure")
		response, err := smokeGet(fixtureClient(t, insecure, smokeTimeout), insecure, "api/server/info", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSmokeInfo(response, ""); err != nil {
			t.Fatal(err)
		}
	})
	for _, sendHeaders := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout_headers_sent_%t", sendHeaders), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sendHeaders {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			target := fixtureTarget(t, server.URL+"|token="+fixtureSecret)
			client := fixtureClient(t, target, 40*time.Millisecond)
			start := time.Now()
			_, err := smokeGet(client, target, "api/health", true)
			requireSecretSafeError(t, err)
			if time.Since(start) > 3*time.Second {
				t.Fatal("request did not respect finite timeout")
			}
		})
	}
	for _, timeout := range []time.Duration{0, -time.Second, smokeTimeout + time.Second} {
		_, err := newSmokeClient(smokeTarget{}, timeout)
		requireSecretSafeError(t, err)
	}
}

func TestSmokeFixtureRedirectAndCredentialBoundary(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, crossOrigin := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_%d_cross_origin_%t", status, crossOrigin), func(t *testing.T) {
				var redirectedCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/health" {
						redirectedCalls.Add(1)
						return
					}
					if r.Header.Get("Authorization") != "Bearer "+fixtureSecret {
						t.Error("expected explicit API bearer")
					}
					location := "/sink?token=" + fixtureSecret
					if crossOrigin {
						location = destination.URL + location
					}
					w.Header().Set("Location", location)
					w.WriteHeader(status)
				}))
				defer server.Close()
				target := fixtureTarget(t, server.URL+"|token="+fixtureSecret)
				_, err := smokeGet(fixtureClient(t, target, smokeTimeout), target, "api/health", true)
				requireSecretSafeError(t, err)
				if redirectedCalls.Load() != 0 || destinationCalls.Load() != 0 {
					t.Fatal("redirect destination received a request")
				}
			})
		}
	}
	target := fixtureTarget(t, destination.URL+"|token="+fixtureSecret)
	client := fixtureClient(t, target, smokeTimeout)
	for _, ref := range []string{"./", "assets/app.js", "api/server/info", "../api/health"} {
		_, err := smokeGet(client, target, ref, true)
		requireSecretSafeError(t, err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer other.Close()
	for _, ref := range []string{other.URL + "/assets/app.js", "/assets/app.js?token=" + fixtureSecret, "/assets/app.js#", "/assets/" + fixtureSecret + ".js"} {
		_, err := smokeGet(client, target, ref, false)
		requireSecretSafeError(t, err)
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("safety boundary allowed an unexpected request")
	}
}

func TestSmokeFixtureSPASameOriginAndMIME(t *testing.T) {
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { externalCalls.Add(1) }))
	defer external.Close()
	for _, tc := range []struct {
		name, scripts, indexMIME, assetMIME, assetBody string
		assetStatus                                    int
		bad                                            bool
	}{
		{"valid", `<script type="module" src="/assets/app.js"></script>`, "text/html", "text/javascript", "console.log('fixture');", 200, false},
		{"relative_single_quote", `<script src='assets/app.js'></script>`, "text/html", "application/javascript", "export {};", 200, false},
		{"unquoted", `<script src=/assets/app.js></script>`, "text/html", "application/javascript", "export {};", 200, false},
		{"cross_origin", `<script src="` + external.URL + `/assets/app.js"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"cross_origin_after_local", `<script src="/assets/app.js"></script><script src="` + external.URL + `/assets/app.js"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"protocol_relative", `<script src="//` + strings.TrimPrefix(external.URL, "http://") + `/assets/app.js"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"query", `<script src="/assets/app.js?token=` + fixtureSecret + `"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"fragment", `<script src="/assets/app.js#"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"base_tag", `<base href="` + external.URL + `/"><script src="/assets/app.js"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"no_script", "", "text/html", "text/javascript", "export {};", 200, true},
		{"source_not_bundle", `<script src="/src/main.tsx"></script>`, "text/html", "text/javascript", "export {};", 200, true},
		{"wrong_index_mime", `<script src="/assets/app.js"></script>`, "text/plain", "text/javascript", "export {};", 200, true},
		{"html_asset_mime", `<script src="/assets/app.js"></script>`, "text/html", "text/html", "<html>login</html>", 200, true},
		{"html_disguised_as_js", `<script src="/assets/app.js"></script>`, "text/html", "text/javascript", "<!doctype html><html>login</html>", 200, true},
		{"empty_asset", `<script src="/assets/app.js"></script>`, "text/html", "text/javascript", "  ", 200, true},
		{"missing_asset", `<script src="/assets/app.js"></script>`, "text/html", "text/javascript", fixtureSecret, 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var assetCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("SPA or asset received credentials")
				}
				if r.URL.Path == "/" {
					w.Header().Set("Content-Type", tc.indexMIME)
					_, _ = io.WriteString(w, `<html><head><title>QuantumAtlas</title></head><body><div id="root"></div>`+tc.scripts+`</body></html>`)
					return
				}
				assetCalls.Add(1)
				w.Header().Set("Content-Type", tc.assetMIME)
				w.WriteHeader(tc.assetStatus)
				_, _ = io.WriteString(w, tc.assetBody)
			}))
			defer server.Close()
			target := fixtureTarget(t, server.URL+"|token="+fixtureSecret)
			err := checkSmokeSPA(fixtureClient(t, target, smokeTimeout), target)
			if tc.bad {
				requireSecretSafeError(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
			if !tc.bad && assetCalls.Load() != 1 {
				t.Fatal("bundled script was not fetched")
			}
			if tc.name == "cross_origin_after_local" && assetCalls.Load() != 0 {
				t.Fatal("fetched a script before validating every source")
			}
			if externalCalls.Load() != 0 {
				t.Fatal("cross-origin script was requested")
			}
		})
	}
}

func TestSmokeFixtureAuthenticatedHTTPAndPrefix(t *testing.T) {
	var authenticatedCalls, assetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("smoke fixture received a non-GET request")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/prefix/api/health":
			if r.Header.Get("Authorization") != "Bearer "+fixtureSecret {
				t.Error("authenticated health did not receive the explicit bearer")
			}
			authenticatedCalls.Add(1)
			_, _ = io.WriteString(w, strings.Replace(fixtureHealth, `"rawstore":{"status":"ok"}`, `"rawstore":{"status":"ok","backend":"s3-router","buckets":["fixture"]}`, 1))
		case "/prefix/api/papers/stats":
			if r.Header.Get("Authorization") != "Bearer "+fixtureSecret {
				t.Error("authenticated stats did not receive the explicit bearer")
			}
			authenticatedCalls.Add(1)
			_, _ = io.WriteString(w, `{"available":true}`)
		case "/prefix/", "/prefix/assets/app.js":
			if r.Header.Get("Authorization") != "" {
				t.Error("SPA prefix request received credentials")
			}
			if r.URL.Path == "/prefix/" {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<title>QuantumAtlas</title><div id="root"></div><script src="assets/app.js"></script>`)
			} else {
				assetCalls.Add(1)
				w.Header().Set("Content-Type", "text/javascript")
				_, _ = io.WriteString(w, "export {};")
			}
		default:
			t.Error("request escaped deployment prefix")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	target := fixtureTarget(t, server.URL+"/prefix|token="+fixtureSecret)
	client := fixtureClient(t, target, smokeTimeout)
	health, err := smokeGet(client, target, "api/health", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSmokeHealth(health, true, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	stats, err := smokeGet(client, target, "api/papers/stats", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSmokeProtected(stats, true); err != nil {
		t.Fatal(err)
	}
	if err := checkSmokeSPA(client, target); err != nil {
		t.Fatal(err)
	}
	if authenticatedCalls.Load() != 2 || assetCalls.Load() != 1 {
		t.Fatal("expected authenticated API and anonymous asset requests were not observed")
	}
}

func TestSmokeFixtureResponseLimitsAndRedaction(t *testing.T) {
	for _, tc := range []struct{ name, mime, body string }{
		{"oversized", "application/json", strings.Repeat("x", smokeBodyLimit+1)},
		{"invalid_mime", "invalid\"" + fixtureSecret, fixtureSecret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			target := fixtureTarget(t, server.URL)
			_, err := smokeGet(fixtureClient(t, target, smokeTimeout), target, "api/health", false)
			requireSecretSafeError(t, err)
		})
	}
}
