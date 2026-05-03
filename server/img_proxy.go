package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// img-proxy implements ADR 0017: a same-origin pass-through for SNS CDN
// images that set Cross-Origin-Resource-Policy: same-origin and would
// otherwise be blocked by browsers as <img src=…> embeds. The proxy
// validates the URL against an allowlist, runs an SSRF guard with a
// re-checking dialer (defense against DNS rebinding), strips the CORP
// header and caps body / time, then streams the image back from this
// origin.

const (
	imgProxyMaxBytes = 10 * 1024 * 1024 // 10 MB body cap (decision 3)
	imgProxyTimeout  = 10 * time.Second // per-request upstream timeout (decision 3)
	imgProxyDialTime = 5 * time.Second  // dial timeout inside the custom transport
)

// imgProxyDefaultHostSuffixes is the production allowlist of host suffixes
// the proxy is willing to fetch (ADR 0017 decision 2). Adding a host
// requires a code change — see the ADR for the rationale on not making
// this config-driven.
var imgProxyDefaultHostSuffixes = []string{
	".cdninstagram.com",
	".fbcdn.net",
	".twimg.com",
	".bsky.social",
	".bsky.app",
}

// imgProxyConfig groups the allowlist and the runtime seams (DNS resolver,
// IP-safety check, HTTP transport, timeout) so tests can inject fakes
// without monkey-patching package globals. Production wiring lives in
// defaultImgProxyConfig.
type imgProxyConfig struct {
	HostSuffixes []string
	IsIPSafe     func(net.IP) bool
	LookupIPs    func(ctx context.Context, host string) ([]net.IP, error)
	Transport    http.RoundTripper
	Timeout      time.Duration
}

// defaultImgProxyConfig builds the production config: real DNS resolver,
// strict IP-safety check, custom transport whose DialContext re-resolves
// and re-checks IPs at connect time (TOCTOU defense against DNS
// rebinding), and the ADR-mandated timeout.
func defaultImgProxyConfig() imgProxyConfig {
	cfg := imgProxyConfig{
		HostSuffixes: imgProxyDefaultHostSuffixes,
		IsIPSafe:     ipIsPublicRoutable,
		LookupIPs: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		Timeout: imgProxyTimeout,
	}
	cfg.Transport = newImgProxyTransport(cfg.IsIPSafe, cfg.LookupIPs)
	return cfg
}

// newImgProxyTransport returns an http.Transport whose DialContext
// re-resolves the hostname and re-applies the IP safety check before
// connecting. This is the TOCTOU defense for DNS rebinding: a hostname
// that resolved to a public IP at allowlist-check time can be made to
// resolve to a private IP by the time the dial happens. KeepAlives are
// disabled so a future request cannot reuse a connection that was
// validated against a stale lookup.
func newImgProxyTransport(isIPSafe func(net.IP) bool, lookupIPs func(context.Context, string) ([]net.IP, error)) *http.Transport {
	dialer := &net.Dialer{Timeout: imgProxyDialTime}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := lookupIPs(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no ips for %s", host)
			}
			for _, ip := range ips {
				if !isIPSafe(ip) {
					return nil, fmt.Errorf("blocked ip: %s", ip)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		DisableKeepAlives: true,
	}
}

// ipIsPublicRoutable reports whether the IP is safe for the img proxy to
// fetch. Rejects loopback, private (RFC 1918 + similar), link-local
// unicast/multicast, multicast, unspecified, and the cloud metadata
// address explicitly (covered by IsLinkLocalUnicast today, but written
// out so a future stdlib relaxation does not silently weaken us).
func ipIsPublicRoutable(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return false
	}
	return true
}

// isHostAllowed reports whether host matches any of the suffixes. The
// leading dot in each suffix makes the match safe against
// `evil.cdninstagram.com.attacker.tld` style attacks: that host would
// only match `.cdninstagram.com` if the suffix were missing the leading
// dot. The apex case (e.g. `cdninstagram.com` without a subdomain) is
// covered by the `host == suffix-without-dot` branch.
func isHostAllowed(host string, suffixes []string) bool {
	host = strings.ToLower(host)
	for _, suf := range suffixes {
		suf = strings.ToLower(suf)
		if strings.HasSuffix(host, suf) || host == strings.TrimPrefix(suf, ".") {
			return true
		}
	}
	return false
}

// newImgProxyHandler returns the http.HandlerFunc for GET /api/v1/img-proxy.
// The handler closes over the supplied config so production and tests can
// share the same logic with different seams. See ADR 0017 for the full
// design.
//
// Failure responses are intentionally empty-bodied with status 502: the
// page-side <img onerror="…"> chain falls back to the letter avatar
// exactly as it does today when the CDN blocks the direct fetch.
func newImgProxyHandler(cfg imgProxyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. parse + validate URL — https only, no http/data/file/relative
		raw := r.URL.Query().Get("url")
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		// 2. allowlist check on the resolved hostname
		host := strings.ToLower(parsed.Hostname())
		if !isHostAllowed(host, cfg.HostSuffixes) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		// 3. SSRF: resolve and reject if any returned IP is unsafe. The
		//    transport's DialContext re-checks at connect time, so this
		//    pre-check is defense-in-depth + early rejection.
		ips, err := cfg.LookupIPs(r.Context(), host)
		if err != nil || len(ips) == 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for _, ip := range ips {
			if !cfg.IsIPSafe(ip) {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}

		// 4. fetch upstream with the configured transport + timeout. The
		//    URL is user-controlled but constrained by (a) https-only scheme
		//    guard, (b) allowlist suffix-match on hostname, (c) SSRF
		//    pre-check on resolved IPs, and (d) the transport's DialContext
		//    re-check at connect time (DNS rebinding defense). gosec sees
		//    only the taint flow and not these mitigations, hence nolint.
		client := &http.Client{Transport: cfg.Transport, Timeout: cfg.Timeout}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, parsed.String(), http.NoBody) //nolint:gosec // ssrf mitigated by allowlist + IP check + dialer re-check
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		req.Header.Set("User-Agent", "newscope-img-proxy/1.0")
		resp, err := client.Do(req) //nolint:gosec // ssrf mitigated; see request creation above
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		// 5. validate upstream response
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(ct), "image/") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		// 6. set headers — explicitly do NOT copy upstream's CORP/COEP/COOP.
		//    go's ResponseWriter starts with empty headers so we just write
		//    the ones we want; the comment is documentation, not enforcement.
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)

		// 7. stream body with size cap. LimitReader returns EOF at the cap
		//    and io.Copy returns whatever was below — we get a truncated
		//    image (browser will likely fail to decode), not a hard error,
		//    which keeps the failure path symmetrical with the onerror
		//    fallback the template already has.
		if _, err := io.Copy(w, io.LimitReader(resp.Body, imgProxyMaxBytes)); err != nil {
			// host is the lowercased Hostname() of an https:// url that
			// passed the allowlist suffix-match against ASCII-only DNS
			// labels — url.Parse rejects host strings with control or
			// CR/LF chars upstream, so log injection via this channel is
			// not reachable. nolint covers gosec's taint-flow conservatism.
			log.Printf("[WARN] img-proxy stream host=%s: %v", host, err) //nolint:gosec // host validated by allowlist; url.Parse rejects ctrl/crlf
		}
	}
}
