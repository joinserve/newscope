package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/umputun/newscope/pkg/domain"
)

const (
	// rsshubRadarPrefix is the public path prefix for the radar reverse proxy.
	// Requests are mapped from <rsshubRadarPrefix>/<rest> to <rsshub.host>/api/radar/<rest>.
	rsshubRadarPrefix = "/api/v1/rsshub/radar"

	// rsshubFetchTimeout is the maximum time spent fetching data from the upstream RSSHub.
	rsshubFetchTimeout = 10 * time.Second

	// rsshubPreviewTimeout bounds how long the preview handler waits for the feed parser.
	rsshubPreviewTimeout = 15 * time.Second

	// rsshubPreviewMaxItems caps the number of items shown in the preview snippet.
	rsshubPreviewMaxItems = 5
)

// rsshubRoute captures the subset of an upstream route definition we care about.
type rsshubRoute struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
}

// rsshubNamespace captures the subset of an upstream namespace definition we care about.
type rsshubNamespace struct {
	Name   string                 `json:"name"`
	URL    string                 `json:"url"`
	Lang   string                 `json:"lang"`
	Routes map[string]rsshubRoute `json:"routes"`
}

// rsshubNamespaceSummary is the response shape for the namespaces listing.
type rsshubNamespaceSummary struct {
	Key        string   `json:"key"`
	Name       string   `json:"name,omitempty"`
	URL        string   `json:"url,omitempty"`
	Lang       string   `json:"lang,omitempty"`
	Categories []string `json:"categories"`
}

// radarProxyHandler proxies RSSHub radar requests to the configured RSSHub host.
// Only the /api/radar/* subtree is exposed, not the full RSSHub API.
func (s *Server) radarProxyHandler(w http.ResponseWriter, r *http.Request) {
	target, err := s.rsshubTarget()
	if err != nil {
		rsshubConfigError(w, err)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)
		suffix := strings.TrimPrefix(req.URL.Path, rsshubRadarPrefix)
		req.URL.Path = "/api/radar" + suffix
		req.Host = target.Host
	}
	proxy.ServeHTTP(w, r)
}

// rsshubCategoriesHandler returns the sorted set of unique categories across all upstream namespaces.
func (s *Server) rsshubCategoriesHandler(w http.ResponseWriter, r *http.Request) {
	data, err := s.fetchRSSHubNamespaces(r.Context())
	if err != nil {
		rsshubUpstreamError(w, err)
		return
	}
	set := make(map[string]struct{})
	for _, ns := range data {
		for _, route := range ns.Routes {
			for _, c := range route.Categories {
				set[c] = struct{}{}
			}
		}
	}
	cats := make([]string, 0, len(set))
	for c := range set {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	writeJSONResponse(w, cats)
}

// rsshubNamespacesHandler lists all namespaces, optionally filtered by ?category=<name>.
func (s *Server) rsshubNamespacesHandler(w http.ResponseWriter, r *http.Request) {
	data, err := s.fetchRSSHubNamespaces(r.Context())
	if err != nil {
		rsshubUpstreamError(w, err)
		return
	}
	filter := r.URL.Query().Get("category")
	out := make([]rsshubNamespaceSummary, 0, len(data))
	for key, ns := range data {
		cats := namespaceCategories(ns)
		if filter != "" && !containsString(cats, filter) {
			continue
		}
		out = append(out, rsshubNamespaceSummary{
			Key:        key,
			Name:       ns.Name,
			URL:        ns.URL,
			Lang:       ns.Lang,
			Categories: cats,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSONResponse(w, out)
}

// rsshubNamespaceDetailHandler proxies a single-namespace lookup to the upstream RSSHub.
func (s *Server) rsshubNamespaceDetailHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, "namespace name required", http.StatusBadRequest)
		return
	}

	target, err := s.rsshubTarget()
	if err != nil {
		rsshubConfigError(w, err)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)
		req.URL.Path = "/api/namespace/" + name
		req.URL.RawQuery = ""
		req.Host = target.Host
	}
	proxy.ServeHTTP(w, r)
}

// rsshubPreviewHandler renders a feed preview snippet for a rsshub:// URL so the user can
// sanity-check a route before saving it. Uses scheduler.ParseFeed so the rsshub:// scheme
// is rewritten through the configured host and actual feed parsing applies.
func (s *Server) rsshubPreviewHandler(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		http.Error(w, "url query parameter required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(rawURL, "rsshub://") {
		http.Error(w, "only rsshub:// urls are supported for preview", http.StatusBadRequest)
		return
	}

	data := struct {
		Error           string
		FeedTitle       string
		FeedDescription string
		Items           []domain.ParsedItem
	}{}

	ctx, cancel := context.WithTimeout(r.Context(), rsshubPreviewTimeout)
	defer cancel()

	parsedFeed, err := s.scheduler.ParseFeed(ctx, rawURL)
	if err != nil {
		data.Error = err.Error()
	} else {
		data.FeedTitle = parsedFeed.Title
		data.FeedDescription = parsedFeed.Description
		data.Items = parsedFeed.Items
		if len(data.Items) > rsshubPreviewMaxItems {
			data.Items = data.Items[:rsshubPreviewMaxItems]
		}
	}

	if err := s.templates.ExecuteTemplate(w, "feed-preview.html", data); err != nil {
		log.Printf("[WARN] failed to render feed preview: %v", err)
	}
}

// rsshubTarget returns the parsed RSSHub base URL or an error if unconfigured.
func (s *Server) rsshubTarget() (*url.URL, error) {
	host := strings.TrimRight(s.config.GetFullConfig().RSSHub.Host, "/")
	if host == "" {
		return nil, fmt.Errorf("rsshub.host is not configured")
	}
	return url.Parse(host)
}

// fetchRSSHubNamespaces retrieves and decodes the upstream /api/namespace payload.
func (s *Server) fetchRSSHubNamespaces(ctx context.Context) (map[string]rsshubNamespace, error) {
	target, err := s.rsshubTarget()
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, rsshubFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target.String()+"/api/namespace", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	out := make(map[string]rsshubNamespace)
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode upstream: %w", err)
	}
	return out, nil
}

// namespaceCategories returns the sorted, deduplicated category list for a namespace.
func namespaceCategories(ns rsshubNamespace) []string {
	set := make(map[string]struct{})
	for _, r := range ns.Routes {
		for _, c := range r.Categories {
			set[c] = struct{}{}
		}
	}
	cats := make([]string, 0, len(set))
	for c := range set {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}

func containsString(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[ERROR] failed to encode rsshub response: %v", err)
	}
}

func rsshubConfigError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}

func rsshubUpstreamError(w http.ResponseWriter, err error) {
	msg := err.Error()
	if strings.Contains(msg, "rsshub.host is not configured") {
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	http.Error(w, msg, http.StatusBadGateway)
}
