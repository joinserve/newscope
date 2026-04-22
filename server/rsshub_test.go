package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/server/mocks"
)

func newRSSHubStub(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newRSSHubServer(t *testing.T, host string) *Server {
	t.Helper()
	return &Server{
		config: &mocks.ConfigProviderMock{
			GetFullConfigFunc: func() *config.Config {
				return &config.Config{RSSHub: config.RSSHubConfig{Host: host}}
			},
		},
	}
}

func sampleNamespacePayload() []byte {
	payload := map[string]rsshubNamespace{
		"cna": {
			Name: "中央通讯社", URL: "cna.com.tw", Lang: "zh-TW",
			Routes: map[string]rsshubRoute{
				"/web/:id?": {Categories: []string{"traditional-media"}},
				"/:id?":     {Categories: []string{"traditional-media"}},
			},
		},
		"bbc": {
			Name: "BBC", URL: "bbc.co.uk", Lang: "en",
			Routes: map[string]rsshubRoute{
				"/news": {Categories: []string{"traditional-media", "new-media"}},
			},
		},
		"github": {
			Name: "GitHub", URL: "github.com", Lang: "en",
			Routes: map[string]rsshubRoute{
				"/repos/:user": {Categories: []string{"programming"}},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func TestServer_radarProxyHandler(t *testing.T) {
	t.Run("proxies and rewrites path", func(t *testing.T) {
		var capturedPath string
		upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer upstream.Close()

		srv := newRSSHubServer(t, upstream.URL)

		req := httptest.NewRequest(http.MethodGet, rsshubRadarPrefix+"/rules.json", http.NoBody)
		rec := httptest.NewRecorder()
		srv.radarProxyHandler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "/api/radar/rules.json", capturedPath)
		body, _ := io.ReadAll(rec.Body)
		assert.JSONEq(t, `{"ok":true}`, string(body))
	})

	t.Run("returns 503 when host not configured", func(t *testing.T) {
		srv := newRSSHubServer(t, "")
		req := httptest.NewRequest(http.MethodGet, rsshubRadarPrefix+"/rules", http.NoBody)
		rec := httptest.NewRecorder()
		srv.radarProxyHandler(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("trims trailing slash on host", func(t *testing.T) {
		var capturedPath string
		upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
		}))
		defer upstream.Close()

		srv := newRSSHubServer(t, upstream.URL+"/")
		req := httptest.NewRequest(http.MethodGet, rsshubRadarPrefix+"/rules", http.NoBody)
		rec := httptest.NewRecorder()
		srv.radarProxyHandler(rec, req)
		assert.Equal(t, "/api/radar/rules", capturedPath)
	})
}

func TestServer_rsshubCategoriesHandler(t *testing.T) {
	var capturedPath string
	upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write(sampleNamespacePayload())
	}))
	defer upstream.Close()

	srv := newRSSHubServer(t, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/categories", http.NoBody)
	rec := httptest.NewRecorder()
	srv.rsshubCategoriesHandler(rec, req)
	assert.Equal(t, "/api/namespace", capturedPath)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	var got []string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []string{"new-media", "programming", "traditional-media"}, got)
}

func TestServer_rsshubNamespacesHandler(t *testing.T) {
	upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sampleNamespacePayload())
	}))
	defer upstream.Close()

	t.Run("returns all namespaces sorted by key", func(t *testing.T) {
		srv := newRSSHubServer(t, upstream.URL)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubNamespacesHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var got []rsshubNamespaceSummary
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got, 3)
		assert.Equal(t, []string{"bbc", "cna", "github"}, []string{got[0].Key, got[1].Key, got[2].Key})
		assert.Equal(t, []string{"new-media", "traditional-media"}, got[0].Categories)
		assert.Equal(t, "BBC", got[0].Name)
	})

	t.Run("filters by category", func(t *testing.T) {
		srv := newRSSHubServer(t, upstream.URL)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces?category=traditional-media", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubNamespacesHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var got []rsshubNamespaceSummary
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		keys := make([]string, 0, len(got))
		for _, s := range got {
			keys = append(keys, s.Key)
		}
		assert.Equal(t, []string{"bbc", "cna"}, keys)
	})

	t.Run("unknown category returns empty list not null", func(t *testing.T) {
		srv := newRSSHubServer(t, upstream.URL)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces?category=does-not-exist", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubNamespacesHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `[]`, rec.Body.String())
	})
}

func TestServer_rsshubNamespaceDetailHandler(t *testing.T) {
	t.Run("proxies single namespace lookup", func(t *testing.T) {
		var capturedPath string
		upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"CNA"}`))
		}))
		defer upstream.Close()

		srv := newRSSHubServer(t, upstream.URL)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces/cna?ignored=1", http.NoBody)
		req.SetPathValue("name", "cna")
		rec := httptest.NewRecorder()
		srv.rsshubNamespaceDetailHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "/api/namespace/cna", capturedPath)
		assert.JSONEq(t, `{"name":"CNA"}`, rec.Body.String())
	})

	t.Run("missing name returns 400", func(t *testing.T) {
		srv := newRSSHubServer(t, "http://example.com")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces/", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubNamespaceDetailHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("returns 503 when host not configured", func(t *testing.T) {
		srv := newRSSHubServer(t, "")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/namespaces/cna", http.NoBody)
		req.SetPathValue("name", "cna")
		rec := httptest.NewRecorder()
		srv.rsshubNamespaceDetailHandler(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestServer_rsshubPreviewHandler(t *testing.T) {
	cfg := &mocks.ConfigProviderMock{
		GetServerConfigFunc: func() (string, time.Duration) { return ":0", 30 * time.Second },
		GetFullConfigFunc: func() *config.Config {
			return &config.Config{RSSHub: config.RSSHubConfig{Host: "http://stub"}}
		},
	}

	t.Run("renders feed snippet on success", func(t *testing.T) {
		scheduler := &mocks.SchedulerMock{
			ParseFeedFunc: func(ctx context.Context, feedURL string) (*domain.ParsedFeed, error) {
				assert.Equal(t, "rsshub://cna/news/aall", feedURL)
				return &domain.ParsedFeed{
					Title:       "CNA News",
					Description: "Central News Agency",
					Items: []domain.ParsedItem{
						{Title: "Story A", Link: "https://cna.com.tw/a", Published: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)},
						{Title: "Story B", Link: "https://cna.com.tw/b"},
					},
				}, nil
			},
		}
		srv := testServer(t, cfg, &mocks.DatabaseMock{}, scheduler)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/preview?url=rsshub://cna/news/aall", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubPreviewHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "Preview: CNA News")
		assert.Contains(t, body, "Central News Agency")
		assert.Contains(t, body, "Story A")
		assert.Contains(t, body, "Story B")
	})

	t.Run("caps items at 5", func(t *testing.T) {
		items := make([]domain.ParsedItem, 10)
		for i := range items {
			items[i] = domain.ParsedItem{Title: "item", Link: "https://x/"}
		}
		scheduler := &mocks.SchedulerMock{
			ParseFeedFunc: func(ctx context.Context, feedURL string) (*domain.ParsedFeed, error) {
				return &domain.ParsedFeed{Title: "T", Items: items}, nil
			},
		}
		srv := testServer(t, cfg, &mocks.DatabaseMock{}, scheduler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/preview?url=rsshub://x/y", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubPreviewHandler(rec, req)
		// template renders one <li> per item; count occurrences via title marker
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 5, strings.Count(rec.Body.String(), `target="_blank"`))
	})

	t.Run("renders error block when parse fails", func(t *testing.T) {
		scheduler := &mocks.SchedulerMock{
			ParseFeedFunc: func(ctx context.Context, feedURL string) (*domain.ParsedFeed, error) {
				return nil, errors.New("upstream 503: NotFoundError")
			},
		}
		srv := testServer(t, cfg, &mocks.DatabaseMock{}, scheduler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/preview?url=rsshub://missing", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubPreviewHandler(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Error fetching feed")
		assert.Contains(t, rec.Body.String(), "NotFoundError")
	})

	t.Run("missing url returns 400", func(t *testing.T) {
		srv := testServer(t, cfg, &mocks.DatabaseMock{}, &mocks.SchedulerMock{})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/preview", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubPreviewHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("rejects non-rsshub scheme", func(t *testing.T) {
		srv := testServer(t, cfg, &mocks.DatabaseMock{}, &mocks.SchedulerMock{})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/preview?url=https://evil.example/feed", http.NoBody)
		rec := httptest.NewRecorder()
		srv.rsshubPreviewHandler(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestServer_rsshubUpstreamFailure(t *testing.T) {
	upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	srv := newRSSHubServer(t, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/rsshub/categories", http.NoBody)
	rec := httptest.NewRecorder()
	srv.rsshubCategoriesHandler(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestServer_rsshubRoutesMounted(t *testing.T) {
	upstream := newRSSHubStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/namespace":
			_, _ = w.Write(sampleNamespacePayload())
		case "/api/namespace/cna":
			_, _ = w.Write([]byte(`{"name":"CNA"}`))
		case "/api/radar/rules":
			_, _ = w.Write([]byte(`["rule1"]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := &mocks.ConfigProviderMock{
		GetServerConfigFunc: func() (string, time.Duration) { return ":0", 30 * time.Second },
		GetFullConfigFunc: func() *config.Config {
			return &config.Config{RSSHub: config.RSSHubConfig{Host: upstream.URL}}
		},
	}
	scheduler := &mocks.SchedulerMock{
		ParseFeedFunc: func(ctx context.Context, feedURL string) (*domain.ParsedFeed, error) {
			return &domain.ParsedFeed{Title: "Mocked", Items: []domain.ParsedItem{{Title: "x", Link: "https://x"}}}, nil
		},
	}
	srv := testServer(t, cfg, &mocks.DatabaseMock{}, scheduler)
	ts := httptest.NewServer(srv.router)
	defer ts.Close()

	cases := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/api/v1/rsshub/radar/rules", http.StatusOK, `["rule1"]`},
		{"/api/v1/rsshub/categories", http.StatusOK, ""},
		{"/api/v1/rsshub/namespaces", http.StatusOK, ""},
		{"/api/v1/rsshub/namespaces?category=programming", http.StatusOK, ""},
		{"/api/v1/rsshub/namespaces/cna", http.StatusOK, `{"name":"CNA"}`},
		{"/api/v1/rsshub/preview?url=rsshub://cna/news", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, tc.wantCode, resp.StatusCode, "body: %s", body)
			if tc.wantBody != "" {
				assert.JSONEq(t, tc.wantBody, string(body))
			}
		})
	}
}

