package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ipIsPublicRoutable + isHostAllowed are pure functions; cover them with
// table-driven unit tests so the handler-level integration tests don't
// need to re-prove every reject branch.

func TestIPIsPublicRoutable(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"public IPv4 is routable", "8.8.8.8", true},
		{"public IPv6 is routable", "2606:4700:4700::1111", true},
		{"loopback IPv4 rejected", "127.0.0.1", false},
		{"loopback IPv6 rejected", "::1", false},
		{"RFC 1918 10/8 rejected", "10.0.0.1", false},
		{"RFC 1918 172.16/12 rejected", "172.16.0.1", false},
		{"RFC 1918 192.168/16 rejected", "192.168.1.1", false},
		{"link-local IPv4 rejected", "169.254.1.1", false},
		{"cloud metadata rejected", "169.254.169.254", false},
		{"link-local IPv6 rejected", "fe80::1", false},
		{"multicast IPv4 rejected", "224.0.0.1", false},
		{"unspecified IPv4 rejected", "0.0.0.0", false},
		{"unspecified IPv6 rejected", "::", false},
		{"nil ip rejected", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ip net.IP
			if tc.ip != "" {
				ip = net.ParseIP(tc.ip)
				require.NotNil(t, ip, "parse %q", tc.ip)
			}
			assert.Equal(t, tc.want, ipIsPublicRoutable(ip))
		})
	}
}

func TestIsHostAllowed(t *testing.T) {
	suffixes := []string{".cdninstagram.com", ".fbcdn.net", ".bsky.app"}
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"cdn subdomain matches", "cdn.cdninstagram.com", true},
		{"deep subdomain matches", "scontent-tpe1-1.cdninstagram.com", true},
		{"apex matches via trim-prefix branch", "cdninstagram.com", true},
		{"case-insensitive", "CDN.CDNInstagram.COM", true},
		{"adjacent CDN matches", "static.fbcdn.net", true},
		{"unrelated host rejected", "evil.com", false},
		{"suffix-injection attack rejected", "cdninstagram.com.evil.com", false},
		{"empty host rejected", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isHostAllowed(tc.host, suffixes))
		})
	}
}

// imgProxyTestConfig builds a permissive baseline config for handler-level
// tests: real production allowlist, IsIPSafe always-true (so the SSRF
// pre-check never trips), LookupIPs returns a fixed public IP. Tests
// override individual fields (Transport, IsIPSafe, LookupIPs, Timeout,
// HostSuffixes) as needed.
func imgProxyTestConfig() imgProxyConfig {
	return imgProxyConfig{
		HostSuffixes: imgProxyDefaultHostSuffixes,
		IsIPSafe:     func(net.IP) bool { return true },
		LookupIPs: func(_ context.Context, _ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return makeUpstreamResponse(http.StatusOK, "image/jpeg", nil, []byte("default-jpeg-bytes")), nil
		}),
		Timeout: 2 * time.Second,
	}
}

func TestImgProxy_AllowlistMatch(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		wantStatus int
	}{
		{"cdn subdomain passes", "cdn.cdninstagram.com", http.StatusOK},
		{"scontent subdomain passes", "scontent-tpe1-1.cdninstagram.com", http.StatusOK},
		{"twimg passes", "pbs.twimg.com", http.StatusOK},
		{"random host rejected", "evil.com", http.StatusBadGateway},
		{"suffix-injection rejected", "cdninstagram.com.evil.com", http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := imgProxyTestConfig()
			h := newImgProxyHandler(cfg)
			req := newImgProxyReq(t, "https://"+tc.host+"/x.jpg")
			w := httptest.NewRecorder()
			h(w, req)
			assert.Equal(t, tc.wantStatus, w.Code, "host %q", tc.host)
		})
	}
}

func TestImgProxy_RejectsNonHTTPS(t *testing.T) {
	bad := []string{
		"http://cdn.cdninstagram.com/x.jpg",
		"data:image/jpeg;base64,AAA",
		"file:///etc/passwd",
		"/relative/path.jpg",
		"",
		"://malformed",
	}
	for _, raw := range bad {
		t.Run(raw, func(t *testing.T) {
			cfg := imgProxyTestConfig()
			h := newImgProxyHandler(cfg)
			req := newImgProxyReq(t, raw)
			w := httptest.NewRecorder()
			h(w, req)
			assert.Equal(t, http.StatusBadGateway, w.Code, "url %q must be rejected", raw)
		})
	}
}

func TestImgProxy_SSRF(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{"loopback IPv4", "127.0.0.1"},
		{"loopback IPv6", "::1"},
		{"RFC 1918 10/8", "10.0.0.1"},
		{"RFC 1918 192.168", "192.168.1.1"},
		{"cloud metadata", "169.254.169.254"},
		{"link-local IPv6", "fe80::1"},
		{"unspecified IPv4", "0.0.0.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transportCalled := false
			cfg := imgProxyTestConfig()
			cfg.IsIPSafe = ipIsPublicRoutable
			cfg.LookupIPs = func(_ context.Context, _ string) ([]net.IP, error) {
				return []net.IP{net.ParseIP(tc.ip)}, nil
			}
			cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
				transportCalled = true
				return makeUpstreamResponse(http.StatusOK, "image/jpeg", nil, []byte("x")), nil
			})
			h := newImgProxyHandler(cfg)
			req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.jpg")
			w := httptest.NewRecorder()
			h(w, req)
			assert.Equal(t, http.StatusBadGateway, w.Code)
			assert.False(t, transportCalled, "transport must not be called when ssrf check rejects")
		})
	}

	t.Run("any unsafe ip in the set rejects (mixed public + private)", func(t *testing.T) {
		// real attack shape: nameserver returns one public + one private,
		// hoping the dialer picks public and a partial check passes. Our
		// handler rejects when ANY ip is unsafe.
		cfg := imgProxyTestConfig()
		cfg.IsIPSafe = ipIsPublicRoutable
		cfg.LookupIPs = func(_ context.Context, _ string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}, nil
		}
		h := newImgProxyHandler(cfg)
		req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.jpg")
		w := httptest.NewRecorder()
		h(w, req)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("empty lookup result rejected", func(t *testing.T) {
		cfg := imgProxyTestConfig()
		cfg.LookupIPs = func(_ context.Context, _ string) ([]net.IP, error) { return nil, nil }
		h := newImgProxyHandler(cfg)
		req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.jpg")
		w := httptest.NewRecorder()
		h(w, req)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("lookup error rejected", func(t *testing.T) {
		cfg := imgProxyTestConfig()
		cfg.LookupIPs = func(_ context.Context, _ string) ([]net.IP, error) {
			return nil, errors.New("dns broken")
		}
		h := newImgProxyHandler(cfg)
		req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.jpg")
		w := httptest.NewRecorder()
		h(w, req)
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})
}

func TestImgProxy_RejectsNonImageContentType(t *testing.T) {
	cfg := imgProxyTestConfig()
	cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeUpstreamResponse(http.StatusOK, "text/html", nil, []byte("<html></html>")), nil
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x")
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusBadGateway, w.Code,
		"non-image upstream content-type must surface as 502")
}

func TestImgProxy_StripsCORP(t *testing.T) {
	cfg := imgProxyTestConfig()
	cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeUpstreamResponse(http.StatusOK, "image/jpeg", http.Header{
			"Cross-Origin-Resource-Policy": []string{"same-origin"},
			"Cross-Origin-Embedder-Policy": []string{"require-corp"},
			"Cross-Origin-Opener-Policy":   []string{"same-origin"},
		}, []byte("fake-jpeg-bytes")), nil
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.jpg")
	w := httptest.NewRecorder()
	h(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Cross-Origin-Resource-Policy"),
		"CORP header from upstream must not appear on the proxy response")
	assert.Empty(t, w.Header().Get("Cross-Origin-Embedder-Policy"),
		"COEP header from upstream must not appear on the proxy response")
	assert.Empty(t, w.Header().Get("Cross-Origin-Opener-Policy"),
		"COOP header from upstream must not appear on the proxy response")
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
}

func TestImgProxy_SetsCacheHeader(t *testing.T) {
	cfg := imgProxyTestConfig()
	cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeUpstreamResponse(http.StatusOK, "image/png", http.Header{
			"Cache-Control": []string{"no-store"}, // upstream tries to forbid caching
		}, []byte("png-bytes")), nil
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/x.png")
	w := httptest.NewRecorder()
	h(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"),
		"proxy must set its own Cache-Control regardless of upstream's")
}

func TestImgProxy_BodySizeCap(t *testing.T) {
	const oversize = imgProxyMaxBytes + 1024 // 10 MB + 1 KB
	cfg := imgProxyTestConfig()
	cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeUpstreamResponse(http.StatusOK, "image/jpeg", nil, make([]byte, oversize)), nil
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/big.jpg")
	w := httptest.NewRecorder()
	h(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.LessOrEqual(t, w.Body.Len(), imgProxyMaxBytes,
		"body must be truncated at the cap; got %d bytes", w.Body.Len())
}

func TestImgProxy_UpstreamTimeout(t *testing.T) {
	// custom transport that respects context cancellation. http.Client.Timeout
	// cancels the request context internally, so a transport that selects on
	// ctx.Done correctly produces a context.DeadlineExceeded error which the
	// handler maps to 502.
	cfg := imgProxyTestConfig()
	cfg.Timeout = 50 * time.Millisecond
	cfg.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			return makeUpstreamResponse(http.StatusOK, "image/jpeg", nil, []byte("late")), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/slow.jpg")
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusBadGateway, w.Code,
		"upstream slower than the proxy timeout must surface as 502")
}

func TestImgProxy_UpstreamNon200(t *testing.T) {
	cfg := imgProxyTestConfig()
	cfg.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return makeUpstreamResponse(http.StatusNotFound, "text/plain", nil, []byte("not found")), nil
	})
	h := newImgProxyHandler(cfg)
	req := newImgProxyReq(t, "https://cdn.cdninstagram.com/missing.jpg")
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

// Transport-level test: the production transport's DialContext must
// re-resolve and re-apply the IP safety check, so a hostname that resolved
// to a public IP at the handler's pre-check can still be blocked at dial
// time if it now resolves to a private IP (DNS rebinding TOCTOU).
func TestImgProxy_TransportRedialsAndChecks(t *testing.T) {
	tr := newImgProxyTransport(
		ipIsPublicRoutable,
		func(_ context.Context, _ string) ([]net.IP, error) {
			// pretend rebinding to a private IP at dial time, after the
			// handler's pre-check (which got a public IP) already passed.
			return []net.IP{net.ParseIP("10.0.0.1")}, nil
		},
	)
	req, _ := http.NewRequest(http.MethodGet, "https://cdn.cdninstagram.com/x.jpg", http.NoBody)
	resp, err := tr.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "DialContext must refuse to dial when the resolved IP is private (DNS rebinding defense)")
	assert.Contains(t, err.Error(), "blocked ip")
}

// helpers --------------------------------------------------------------

// roundTripperFunc adapts a function to http.RoundTripper for tests that
// want to intercept the upstream call without standing up an httptest
// server (which would force HTTP-only and conflict with the handler's
// https-only scheme guard).
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func makeUpstreamResponse(status int, contentType string, extra http.Header, body []byte) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	for k, vv := range extra {
		for _, v := range vv {
			h.Add(k, v)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

// newImgProxyReq builds a GET /api/v1/img-proxy?url=… request; the URL
// is the raw upstream the handler would proxy to.
func newImgProxyReq(t *testing.T, upstream string) *http.Request {
	t.Helper()
	q := "url=" + url.QueryEscape(upstream)
	return httptest.NewRequest(http.MethodGet, "/api/v1/img-proxy?"+q, http.NoBody)
}
