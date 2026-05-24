package registry

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/koji-1009/gnpm/internal/core"
	"github.com/koji-1009/gnpm/internal/npmrc"
)

const (
	slimAccept = "application/vnd.npm.install-v1+json;q=1.0, application/json;q=0.5"
	fullAccept = "application/json"
)

// Options configures a registry Client.
type Options struct {
	Config        *npmrc.Config
	Cache         *Cache
	UserAgent     string
	Offline       bool
	PreferOffline bool
	HTTP          *http.Client
}

// Client fetches packuments (with ETag/Cache-Control revalidation) and
// tarballs (with integrity verification) from the npm registry.
type Client struct {
	config        *npmrc.Config
	cache         *Cache
	userAgent     string
	offline       bool
	preferOffline bool
	http          *http.Client

	mu        sync.Mutex
	inProcess map[string]*CachedPackument
}

// NewClient builds a Client. A nil HTTP client gets a default with a
// per-host connection limit matching the install fetch concurrency.
func NewClient(opts Options) *Client {
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				// Pool sizing is for the HTTP/1.1 fallback (some private
				// registries are H1.1-only): one socket per in-flight request.
				// Against the public registry the transport negotiates HTTP/2,
				// where requests instead multiplex as streams over ~one
				// connection — the pool ceiling is then not the binding limit;
				// HTTPConcurrency bounds the in-flight streams.
				MaxIdleConns:        core.HTTPConcurrency,
				MaxConnsPerHost:     core.HTTPConcurrency,
				MaxIdleConnsPerHost: core.HTTPConcurrency,
				IdleConnTimeout:     30 * time.Second,
				// HTTP/2 health check: a long install multiplexes every fetch
				// onto one connection, so a silently dropped conn (NAT / idle
				// timeout) would hang all in-flight streams until the client
				// Timeout. Ping an idle connection and recycle a dead one in
				// ~30s instead.
				HTTP2: &http.HTTP2Config{
					SendPingTimeout: 15 * time.Second,
					PingTimeout:     15 * time.Second,
				},
			},
		}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "gnpm"
	}
	return &Client{
		config:        opts.Config,
		cache:         opts.Cache,
		userAgent:     ua,
		offline:       opts.Offline,
		preferOffline: opts.PreferOffline,
		http:          hc,
		inProcess:     map[string]*CachedPackument{},
	}
}

// Packument fetches name's packument. When requirePublishTimes is set,
// the full (non-slim) form is requested because the slim format omits
// the per-version `time` map needed by the minimum-release-age filter.
func (c *Client) Packument(ctx context.Context, name string, requirePublishTimes bool) (*Packument, error) {
	u := c.packumentURL(name)
	usable := func(p *Packument) bool {
		return !requirePublishTimes || len(p.PublishTimes) > 0
	}

	c.mu.Lock()
	ip := c.inProcess[name]
	c.mu.Unlock()
	if ip != nil && (c.preferOffline || ip.IsFresh()) && usable(ip.Packument) {
		return ip.Packument, nil
	}

	if ip == nil && c.cache != nil {
		if disk, _ := c.cache.ReadPackument(name); disk != nil {
			c.mu.Lock()
			c.inProcess[name] = disk
			c.mu.Unlock()
			ip = disk
			if (c.preferOffline || c.offline || disk.IsFresh()) && usable(disk.Packument) {
				return disk.Packument, nil
			}
		}
	}

	if c.offline {
		return nil, core.NetworkError("--offline: no cached packument for %s", name)
	}

	// At most two passes: a conditional GET, then a forced full GET when a
	// 304 returns a cached body lacking the publish times we need.
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		cached := c.inProcess[name]
		c.mu.Unlock()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, core.NetworkError("building request for %s: %v", name, err)
		}
		c.setAuth(req, u)
		if requirePublishTimes {
			req.Header.Set("Accept", fullAccept)
		} else {
			req.Header.Set("Accept", slimAccept)
		}
		if cached != nil && cached.Etag != "" {
			req.Header.Set("If-None-Match", cached.Etag)
		}
		if cached != nil && cached.LastModified != "" {
			req.Header.Set("If-Modified-Since", cached.LastModified)
		}

		resp, err := c.doRetry(req)
		if err != nil {
			return nil, core.NetworkError("GET %s: %v", u, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, core.NetworkError("reading %s: %v", u, readErr)
		}

		if resp.StatusCode == http.StatusNotModified && cached != nil {
			if requirePublishTimes && len(cached.Packument.PublishTimes) == 0 && attempt == 0 {
				cached.Etag = ""
				cached.LastModified = ""
				continue
			}
			if fresh := freshUntil(resp.Header); !fresh.IsZero() {
				cached.FreshUntil = fresh
				if c.cache != nil {
					_ = c.cache.WritePackument(cached)
				}
			}
			return cached.Packument, nil
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, &core.Error{Kind: core.KindNetwork, Message: "package not found: " + name, StatusCode: 404, URI: u.String()}
		}
		if resp.StatusCode >= 400 {
			return nil, &core.Error{Kind: core.KindNetwork, Message: "GET " + u.String() + " failed", StatusCode: resp.StatusCode, URI: u.String()}
		}

		p, err := ParsePackument(body)
		if err != nil {
			return nil, err
		}
		entry := &CachedPackument{
			Packument:    p,
			Etag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			FreshUntil:   freshUntil(resp.Header),
		}
		c.mu.Lock()
		c.inProcess[name] = entry
		c.mu.Unlock()
		if c.cache != nil {
			_ = c.cache.WritePackument(entry)
		}
		return p, nil
	}
	return nil, core.NetworkError("packument fetch exhausted for %s", name)
}

// Tarball downloads and integrity-verifies a tarball, returning its raw
// bytes. A cache hit that passes verification skips the network.
func (c *Client) Tarball(ctx context.Context, tarballURL, integrity string) ([]byte, error) {
	expected, err := ParseIntegrity(integrity)
	if err != nil {
		return nil, err
	}
	if c.cache != nil {
		if cached, ok := c.cache.ReadTarball(integrity); ok {
			if expected.Verify(cached) == nil {
				return cached, nil
			}
		}
	}
	if c.offline {
		return nil, core.NetworkError("--offline: no cached tarball for %s", tarballURL)
	}
	u, err := url.Parse(tarballURL)
	if err != nil {
		return nil, core.NetworkError("invalid tarball url %q: %v", tarballURL, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tarballURL, nil)
	if err != nil {
		return nil, core.NetworkError("building tarball request: %v", err)
	}
	c.setAuth(req, u)
	resp, err := c.doRetry(req)
	if err != nil {
		return nil, core.NetworkError("GET %s: %v", tarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &core.Error{Kind: core.KindNetwork, Message: "GET " + tarballURL + " failed", StatusCode: resp.StatusCode, URI: tarballURL}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, core.NetworkError("reading tarball %s: %v", tarballURL, err)
	}
	if err := expected.Verify(body); err != nil {
		return nil, err
	}
	if c.cache != nil {
		_ = c.cache.WriteTarball(integrity, body)
	}
	return body, nil
}

// --- url + auth helpers ----------------------------------------------

func (c *Client) registryForName(name string) string {
	if strings.HasPrefix(name, "@") {
		scope := name
		if slash := strings.IndexByte(name, '/'); slash > 0 {
			scope = name[:slash]
		}
		if r := c.config.RegistryFor(scope); r != "" {
			return r
		}
	}
	return c.config.Registry()
}

func (c *Client) packumentURL(name string) *url.URL {
	base := c.registryForName(name)
	path := name
	if strings.HasPrefix(name, "@") {
		// npm encodes the scope separator slash: @scope/pkg → @scope%2fpkg.
		path = strings.Replace(name, "/", "%2f", 1)
	}
	full := strings.TrimRight(base, "/") + "/" + path
	u, err := url.Parse(full)
	if err != nil {
		// Fall back to a best-effort opaque URL; the request will fail
		// cleanly downstream with a NetworkError.
		return &url.URL{Path: full}
	}
	return u
}

func (c *Client) setAuth(req *http.Request, registryURL *url.URL) {
	req.Header.Set("User-Agent", c.userAgent)
	if token := c.config.AuthTokenFor(registryURL); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	if user, pass, ok := c.config.BasicAuthFor(registryURL); ok {
		enc := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req.Header.Set("Authorization", "Basic "+enc)
		return
	}
	if legacy := c.config.LegacyAuthFor(registryURL); legacy != "" {
		req.Header.Set("Authorization", "Basic "+legacy)
	}
}

// doRetry issues req with exponential backoff on transient failures
// (connection errors, HTTP 429, and 5xx). GET requests carry no body, so
// re-issuing is safe. 4xx other than 429 returns immediately.
func (c *Client) doRetry(req *http.Request) (*http.Response, error) {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff(attempt)):
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = core.NetworkError("transient HTTP %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func backoff(attempt int) time.Duration {
	// 100ms, 200ms, 400ms, …
	d := 100 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

var maxAgeRe = regexp.MustCompile(`max-age\s*=\s*(\d+)`)

// freshUntil derives the Cache-Control max-age deadline from the
// response headers, or the zero time when none applies.
func freshUntil(h http.Header) time.Time {
	cc := strings.ToLower(h.Get("Cache-Control"))
	if cc == "" || strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		return time.Time{}
	}
	m := maxAgeRe.FindStringSubmatch(cc)
	if m == nil {
		return time.Time{}
	}
	secs, err := strconv.Atoi(m[1])
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(secs) * time.Second)
}
