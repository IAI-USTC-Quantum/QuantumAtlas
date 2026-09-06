package downloader

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// robotsCache caches a parsed robots.txt per host (10 min TTL) and
// answers Allowed(u) for our fetcher. The parser is a deliberately
// minimal subset of the robots exclusion protocol: User-agent groups
// (with `*` matching our product token), Disallow/Allow prefix rules
// with longest-match-wins (Google semantics), and Crawl-delay (parsed,
// surfaced for operators via the trace detail). Fail-open on robots
// fetch errors — this is on-demand entitled traffic, not a crawl, and a
// hostile robots.txt (some publishers 403 it) must not break fetching.
type robotsCache struct {
	client *http.Client
	ua     string

	mu     sync.Mutex
	byHost map[string]*robotsRules
}

type robotsRules struct {
	group      *robotsRulesGroup // nil = no rules for us
	fetchedAt  time.Time
	crawlDelay time.Duration
}

type robotsRulesGroup struct {
	rules []robotsRuleEntry
}

type robotsRuleEntry struct {
	path    string
	allowed bool
}

const (
	robotsTTL     = 10 * time.Minute
	robotsMaxBody = 256 * 1024
	// robotsAgentToken is the product token matched inside User-agent
	// groups; falls back to the universal `*` group.
	robotsAgentToken = "qatlasdownloader"
)

func newRobotsCache(client *http.Client, ua string) *robotsCache {
	return &robotsCache{client: client, ua: ua, byHost: map[string]*robotsRules{}}
}

// Allowed reports whether GET u is permitted by the host's robots.txt.
// An error is returned only for caller-side bugs (unparseable URL); all
// robots-fetch failures fail open (allowed).
func (r *robotsCache) Allowed(ctx context.Context, u *url.URL) (bool, error) {
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return false, ErrRobots
	}
	rules := r.rulesFor(ctx, u)
	if rules == nil || rules.group == nil {
		return true, nil
	}
	return robotsAllows(rules.group, u.Path), nil
}

// rulesFor returns the cached (or freshly fetched) rules for u's host.
func (r *robotsCache) rulesFor(ctx context.Context, u *url.URL) *robotsRules {
	r.mu.Lock()
	rules, ok := r.byHost[u.Host]
	if ok && time.Since(rules.fetchedAt) < robotsTTL {
		r.mu.Unlock()
		return rules
	}
	r.mu.Unlock()

	fresh := r.fetchRules(ctx, u)
	r.mu.Lock()
	r.byHost[u.Host] = fresh
	r.mu.Unlock()
	return fresh
}

func (r *robotsCache) fetchRules(ctx context.Context, u *url.URL) *robotsRules {
	out := &robotsRules{fetchedAt: time.Now()}
	robotsURL := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL.String(), nil)
	if err != nil {
		return out
	}
	req.Header.Set("User-Agent", r.ua)
	resp, err := r.client.Do(req)
	if err != nil {
		return out // fail open
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out // 403/404/…: no rules → allow
	}
	body := make([]byte, robotsMaxBody)
	n, _ := resp.Body.Read(body)
	out.group, out.crawlDelay = parseRobots(string(body[:n]))
	return out
}

// parseRobots extracts the group applying to our agent token (or `*`)
// and the crawl delay. Nil group = nothing disallows us.
func parseRobots(text string) (*robotsRulesGroup, time.Duration) {
	type rawGroup struct {
		agents   []string
		rules    []robotsRuleEntry
		delay    time.Duration
		hasRules bool
	}

	var groups []*rawGroup
	var cur *rawGroup
	newGroup := func(agent string) *rawGroup {
		g := &rawGroup{agents: []string{agent}}
		groups = append(groups, g)
		return g
	}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			// Consecutive User-agent lines share one group; a rule line
			// in between terminates it.
			if cur != nil && cur.hasRules {
				cur = nil
			}
			if cur == nil {
				cur = newGroup(value)
			} else {
				cur.agents = append(cur.agents, value)
			}
		case "disallow", "allow":
			if cur == nil {
				continue
			}
			cur.hasRules = true
			cur.rules = append(cur.rules, robotsRuleEntry{path: value, allowed: key == "allow"})
		case "crawl-delay":
			if cur == nil {
				continue
			}
			if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
				cur.delay = time.Duration(secs * float64(time.Second))
			}
		}
	}

	// Prefer the group naming our token; else the `*` group.
	var star *rawGroup
	for _, g := range groups {
		for _, a := range g.agents {
			switch strings.ToLower(a) {
			case robotsAgentToken:
				return &robotsRulesGroup{rules: g.rules}, g.delay
			case "*":
				star = g
			}
		}
	}
	if star != nil {
		return &robotsRulesGroup{rules: star.rules}, star.delay
	}
	return nil, 0
}

// robotsAllows implements longest-prefix-match: the most specific
// (longest) matching Allow/Disallow rule wins; default is allow.
func robotsAllows(g *robotsRulesGroup, path string) bool {
	best := -1
	allowed := true
	for _, rule := range g.rules {
		if rule.path == "" {
			continue
		}
		if strings.HasPrefix(path, rule.path) && len(rule.path) >= best {
			best, allowed = len(rule.path), rule.allowed
		}
	}
	return allowed
}
