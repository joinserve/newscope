package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-pkgz/routegroup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/server/mocks"
)

// testServer creates a server instance using the actual New function
func testServer(t *testing.T, cfg ConfigProvider, database Database, scheduler Scheduler) *Server {
	stubBigTags(database)
	return New(cfg, database, scheduler, "test", false)
}

// stubBigTags supplies no-op stubs for expensive side-channel lookups so that
// handler tests don't need to stub these methods just to survive initialization.
func stubBigTags(database Database) {
	m, ok := database.(*mocks.DatabaseMock)
	if !ok {
		return
	}
	if m.GetBigTagsFunc == nil {
		m.GetBigTagsFunc = func(ctx context.Context, threshold int) (map[string]int, error) {
			return map[string]int{}, nil
		}
	}
	if m.ListGroupingsFunc == nil {
		m.ListGroupingsFunc = func(ctx context.Context) ([]domain.Grouping, error) {
			return nil, nil
		}
	}
	if m.GroupingCountsFunc == nil {
		m.GroupingCountsFunc = func(ctx context.Context) (map[int64]int, error) {
			return map[int64]int{}, nil
		}
	}
	if m.GetGroupingBySlugFunc == nil {
		m.GetGroupingBySlugFunc = func(ctx context.Context, slug string) (domain.Grouping, error) {
			return domain.Grouping{}, fmt.Errorf("not found")
		}
	}
	if m.ListTitleRevisionsFunc == nil {
		m.ListTitleRevisionsFunc = func(ctx context.Context, beatID int64) ([]domain.TitleRevision, error) {
			return nil, nil
		}
	}
}

func TestServer_New(t *testing.T) {
	cfg := &mocks.ConfigProviderMock{
		GetFullConfigFunc: func() *config.Config { return &config.Config{} },
		GetServerConfigFunc: func() (string, time.Duration) {
			return ":8080", 30 * time.Second
		},
	}
	database := &mocks.DatabaseMock{}
	scheduler := &mocks.SchedulerMock{
		TriggerPreferenceUpdateFunc: func() {
			// do nothing in tests
		},
	}

	stubBigTags(database)
	srv := New(cfg, database, scheduler, "1.0.0", false)
	assert.NotNil(t, srv)
	assert.Equal(t, "1.0.0", srv.version)
	assert.False(t, srv.debug)
}

func TestServer_Run(t *testing.T) {
	// find free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	err = listener.Close()
	require.NoError(t, err)

	cfg := &mocks.ConfigProviderMock{
		GetFullConfigFunc: func() *config.Config { return &config.Config{} },
		GetServerConfigFunc: func() (string, time.Duration) {
			return fmt.Sprintf("127.0.0.1:%d", port), 30 * time.Second
		},
	}

	database := &mocks.DatabaseMock{
		GetFeedsFunc: func(ctx context.Context) ([]domain.Feed, error) {
			return []domain.Feed{}, nil
		},
		GetItemsFunc: func(ctx context.Context, limit, offset int) ([]domain.Item, error) {
			return []domain.Item{}, nil
		},
	}

	scheduler := &mocks.SchedulerMock{}

	stubBigTags(database)
	srv := New(cfg, database, scheduler, "1.0.0", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// start server in background
	go func() {
		_ = srv.Run(ctx)
	}()

	// wait for server to start
	time.Sleep(100 * time.Millisecond)

	// make test request
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", port))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(body))

	// shutdown server
	cancel()
	time.Sleep(100 * time.Millisecond)
}

func TestGeneratePageNumbers(t *testing.T) {
	tests := []struct {
		name        string
		currentPage int
		totalPages  int
		expected    []int
	}{
		{
			name:        "single page",
			currentPage: 1,
			totalPages:  1,
			expected:    []int{1},
		},
		{
			name:        "three pages, on first",
			currentPage: 1,
			totalPages:  3,
			expected:    []int{1, 2, 3},
		},
		{
			name:        "five pages, on third",
			currentPage: 3,
			totalPages:  5,
			expected:    []int{1, 2, 3, 4, 5},
		},
		{
			name:        "ten pages, on fifth",
			currentPage: 5,
			totalPages:  10,
			expected:    []int{3, 4, 5, 6, 7},
		},
		{
			name:        "ten pages, on first",
			currentPage: 1,
			totalPages:  10,
			expected:    []int{1, 2, 3, 4, 5},
		},
		{
			name:        "ten pages, on last",
			currentPage: 10,
			totalPages:  10,
			expected:    []int{6, 7, 8, 9, 10},
		},
		{
			name:        "zero pages",
			currentPage: 1,
			totalPages:  0,
			expected:    []int{},
		},
		{
			name:        "negative pages",
			currentPage: 1,
			totalPages:  -1,
			expected:    []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePageNumbers(tt.currentPage, tt.totalPages)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatCardTime(t *testing.T) {
	now := time.Now()
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterday := today.AddDate(0, 0, -1)
	lastYear := today.AddDate(-1, 0, 0)

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{name: "zero time returns empty", in: time.Time{}, want: ""},
		{name: "30 seconds ago is 剛剛", in: now.Add(-30 * time.Second), want: "剛剛"},
		{name: "5 minutes ago", in: now.Add(-5 * time.Minute), want: "5 分鐘前"},
		{
			name: "earlier today renders HH:mm",
			in:   today.Add(8 * time.Hour),
			want: today.Add(8 * time.Hour).Format("15:04"),
		},
		{
			name: "yesterday renders M/D",
			in:   yesterday.Add(23 * time.Hour),
			want: yesterday.Add(23 * time.Hour).Format("1/2"),
		},
		{
			name: "last year renders YYYY/M/D",
			in:   lastYear,
			want: lastYear.Format("2006/1/2"),
		},
		{
			name: "exactly one hour ago promotes from 分鐘前 to today/HH:mm",
			in:   now.Add(-time.Hour),
			// either "HH:mm" (still today) or "1/2" (rolled over midnight); both are
			// well-formed outputs of formatCardTime — we just confirm it's not the
			// "分鐘前" tier
			want: "_skip_strict_",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCardTime(tc.in)
			if tc.want == "_skip_strict_" {
				assert.NotContains(t, got, "分鐘前", "1h boundary must leave the minutes tier")
				assert.NotEqual(t, "剛剛", got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("midnight boundary distinguishes today from yesterday", func(t *testing.T) {
		oneSecondBeforeMidnight := today.Add(-time.Second)
		justAfterMidnight := today.Add(time.Second)

		// pre-midnight time falls into the "yesterday" branch → M/D format
		assert.Equal(t, oneSecondBeforeMidnight.Format("1/2"), formatCardTime(oneSecondBeforeMidnight))
		// post-midnight time falls into the "today" branch → HH:mm format
		assert.Equal(t, justAfterMidnight.Format("15:04"), formatCardTime(justAfterMidnight))
	})
}

func TestPostingFrequency(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  string
	}{
		{name: "zero hides line", count: 0, want: ""},
		{name: "negative hides line", count: -5, want: ""},
		{name: "single item rounds up to 1/week", count: 1, want: "約 1/週"},
		{name: "low rate stays in weekly tier", count: 10, want: "約 2/週"},
		{name: "boundary 30 items in 30 days = 1/day", count: 30, want: "約 1/日"},
		{name: "ten per day", count: 300, want: "約 10/日"},
		{name: "23/day still daily tier", count: 690, want: "約 23/日"},
		{name: "boundary exactly 24/day flips to hourly", count: 720, want: "約 1.0/小時"},
		{name: "hourly with 1 decimal", count: 1080, want: "約 1.5/小時"},
		{name: "hourly drops decimal at >= 10", count: 7200, want: "約 10/小時"},
		{name: "high hourly rounds to integer", count: 36000, want: "約 50/小時"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, postingFrequency(tc.count))
		})
	}
}

func TestServer_GetPageSize(t *testing.T) {
	cfg := &mocks.ConfigProviderMock{
		GetFullConfigFunc: func() *config.Config {
			return &config.Config{
				Server: struct {
					Listen   string        `yaml:"listen" json:"listen" jsonschema:"default=:8080,description=HTTP server listen address"`
					Timeout  time.Duration `yaml:"timeout" json:"timeout" jsonschema:"default=30s,description=HTTP server timeout"`
					PageSize int           `yaml:"page_size" json:"page_size" jsonschema:"default=50,minimum=1,description=Articles per page for pagination"`
					BaseURL  string        `yaml:"base_url" json:"base_url" jsonschema:"default=http://localhost:8080,description=Base URL for RSS feeds and external links"`
				}{
					PageSize: 25,
				},
			}
		},
	}

	database := &mocks.DatabaseMock{}
	scheduler := &mocks.SchedulerMock{}

	srv := &Server{
		config:    cfg,
		db:        database,
		scheduler: scheduler,
	}

	pageSize := srv.GetPageSize()
	assert.Equal(t, 25, pageSize)
}

func TestServer_SafeHTML(t *testing.T) {
	// test bluemonday sanitization through template rendering
	tests := []struct {
		name        string
		input       string
		contains    []string // what should be in the output
		notContains []string // what should NOT be in the output
	}{
		{
			name:     "safe HTML preserved",
			input:    `<p>Hello <strong>world</strong></p>`,
			contains: []string{`<p>Hello <strong>world</strong></p>`},
		},
		{
			name:        "script tag removed",
			input:       `<p>Hello</p><script>alert('xss')</script>`,
			contains:    []string{`<p>Hello</p>`},
			notContains: []string{`<script>`, `alert`},
		},
		{
			name:        "onclick attribute removed",
			input:       `<p onclick="alert('xss')">Click me</p>`,
			contains:    []string{`<p>Click me</p>`},
			notContains: []string{`onclick`, `alert`},
		},
		{
			name:     "safe attributes preserved",
			input:    `<a href="https://example.com" title="Example">Link</a>`,
			contains: []string{`href="https://example.com"`, `title="Example"`, `Link</a>`},
		},
		{
			name:        "javascript URL sanitized",
			input:       `<a href="javascript:alert('xss')">Bad Link</a>`,
			contains:    []string{`Bad Link</a>`},
			notContains: []string{`alert('xss')`}, // the dangerous part should be escaped
		},
		{
			name:     "class attributes on allowed elements",
			input:    `<div class="content"><p class="highlight">Text</p></div>`,
			contains: []string{`<div class="content">`, `<p class="highlight">`, `Text</p></div>`},
		},
		{
			name:     "blockquote and cite preserved",
			input:    `<blockquote><p>Quote</p><cite>Author</cite></blockquote>`,
			contains: []string{`<blockquote>`, `<p>Quote</p>`, `<cite>Author</cite>`, `</blockquote>`},
		},
	}

	cfg := &mocks.ConfigProviderMock{
		GetFullConfigFunc: func() *config.Config { return &config.Config{} },
		GetServerConfigFunc: func() (string, time.Duration) {
			return ":8080", 30 * time.Second
		},
	}
	database := &mocks.DatabaseMock{}
	scheduler := &mocks.SchedulerMock{}

	stubBigTags(database)
	srv := New(cfg, database, scheduler, "1.0.0", false)

	// create a simple template to test the safeHTML function
	tmpl, err := srv.templates.New("test").Parse(`{{.Content | safeHTML}}`)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			err := tmpl.Execute(&buf, map[string]string{"Content": tt.input})
			require.NoError(t, err)

			result := buf.String()

			for _, expected := range tt.contains {
				assert.Contains(t, result, expected)
			}

			for _, notExpected := range tt.notContains {
				assert.NotContains(t, result, notExpected)
			}
		})
	}
}

func TestServer_respondWithError(t *testing.T) {
	tests := []struct {
		name           string
		code           int
		message        string
		err            error
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "with error",
			code:           http.StatusInternalServerError,
			message:        "Something went wrong",
			err:            errors.New("database error"),
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Something went wrong\n",
		},
		{
			name:           "without error",
			code:           http.StatusBadRequest,
			message:        "Invalid request",
			err:            nil,
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request\n",
		},
		{
			name:           "not found with error",
			code:           http.StatusNotFound,
			message:        "Resource not found",
			err:            errors.New("item not found"),
			expectedStatus: http.StatusNotFound,
			expectedBody:   "Resource not found\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &Server{
				config:    &mocks.ConfigProviderMock{},
				db:        &mocks.DatabaseMock{},
				scheduler: &mocks.SchedulerMock{},
				router:    routegroup.New(http.NewServeMux()),
			}

			w := httptest.NewRecorder()
			srv.respondWithError(w, tt.code, tt.message, tt.err)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Equal(t, tt.expectedBody, w.Body.String())
		})
	}
}

func TestServer_BeatsRouteNotMountedWhenFeatureOff(t *testing.T) {
	cfg := &mocks.ConfigProviderMock{
		GetServerConfigFunc: func() (string, time.Duration) {
			return ":8080", 30 * time.Second
		},
		GetFullConfigFunc: func() *config.Config {
			return &config.Config{}
		},
	}

	database := &mocks.DatabaseMock{}
	scheduler := &mocks.SchedulerMock{
		TriggerPreferenceUpdateFunc: func() {},
	}

	stubBigTags(database)
	srv := New(cfg, database, scheduler, "test", false)

	// /api/v1/beats must not be mounted when embedding.provider is empty
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beats", http.NoBody)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "beats route must not be mounted when feature is off")
}
