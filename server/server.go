package server

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-pkgz/lgr"
	"github.com/go-pkgz/rest"
	"github.com/go-pkgz/rest/logger"
	"github.com/go-pkgz/routegroup"
	"github.com/microcosm-cc/bluemonday"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/pkg/features"
)

const (
	// server configuration
	defaultThrottleLimit = 100
	defaultSizeLimit     = 1024 * 1024 // 1MB

	// RSS feed defaults
	defaultMinScore = 5.0
	defaultRSSLimit = 100
	defaultBaseURL  = "http://localhost:8080"
)

//go:generate moq -out mocks/config.go -pkg mocks -skip-ensure -fmt goimports . ConfigProvider
//go:generate moq -out mocks/database.go -pkg mocks -skip-ensure -fmt goimports . Database
//go:generate moq -out mocks/scheduler.go -pkg mocks -skip-ensure -fmt goimports . Scheduler

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/css/* static/img/*
var staticFS embed.FS

// bigTagsCache caches the set of "big" tags (those appearing in ≥threshold items).
type bigTagsCache struct {
	mu      sync.RWMutex
	tags    map[string]int
	expires time.Time
}

// Server represents HTTP server instance
type Server struct {
	config         ConfigProvider
	db             Database
	scheduler      Scheduler
	groupingEngine GroupingEngine // may be nil when beats feature is disabled
	version        string
	debug          bool
	templates      *template.Template
	pageTemplates  map[string]*template.Template
	router         *routegroup.Bundle
	bigTags        *bigTagsCache
}

// Database interface for server operations
type Database interface {
	GetFeeds(ctx context.Context) ([]domain.Feed, error)
	GetItems(ctx context.Context, limit, offset int) ([]domain.Item, error)
	GetClassifiedItems(ctx context.Context, minScore float64, topic string, limit int) ([]domain.ClassifiedItem, error)
	GetClassifiedItemsWithFilters(ctx context.Context, req domain.ArticlesRequest) ([]domain.ClassifiedItem, error)
	GetClassifiedItemsCount(ctx context.Context, req domain.ArticlesRequest) (int, error)
	GetClassifiedItem(ctx context.Context, itemID int64) (*domain.ClassifiedItem, error)
	UpdateItemFeedback(ctx context.Context, itemID int64, feedback string) error
	GetTopics(ctx context.Context) ([]string, error)
	GetTopicsFiltered(ctx context.Context, minScore float64) ([]string, error)
	GetTopTopicsByScore(ctx context.Context, minScore float64, limit int) ([]domain.TopicWithScore, error)
	GetActiveFeedNames(ctx context.Context, minScore float64) ([]string, error)
	GetAllFeeds(ctx context.Context) ([]domain.Feed, error)
	CreateFeed(ctx context.Context, feed *domain.Feed) error
	UpdateFeed(ctx context.Context, feedID int64, title, feedURL, iconURL string, fetchInterval time.Duration) error
	UpdateFeedStatus(ctx context.Context, feedID int64, enabled bool) error
	DeleteFeed(ctx context.Context, feedID int64) error
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	SearchItems(ctx context.Context, searchQuery string, req domain.ArticlesRequest) ([]domain.ClassifiedItem, error)
	GetSearchItemsCount(ctx context.Context, searchQuery string, req domain.ArticlesRequest) (int, error)
	ListBeats(ctx context.Context, groupingID *int64, topic string, limit, offset int) ([]domain.BeatWithMembers, error)
	SetFeedback(ctx context.Context, beatID int64, feedback string) error
	GetBeat(ctx context.Context, beatID int64) (domain.BeatWithMembers, error)
	MarkViewed(ctx context.Context, beatID int64) error
	SearchBeatsWithMembers(ctx context.Context, query string, limit int) ([]domain.BeatWithMembers, error)
	GetBigTags(ctx context.Context, threshold int) (map[string]int, error)
	// grouping CRUD
	ListGroupings(ctx context.Context) ([]domain.Grouping, error)
	GetGrouping(ctx context.Context, id int64) (domain.Grouping, error)
	GetGroupingBySlug(ctx context.Context, slug string) (domain.Grouping, error)
	CreateGrouping(ctx context.Context, g domain.Grouping) (int64, error)
	UpdateGrouping(ctx context.Context, g domain.Grouping) error
	DeleteGrouping(ctx context.Context, id int64) error
	ReorderGroupings(ctx context.Context, idsInOrder []int64) error
	// grouping counts for dropdown
	GroupingCounts(ctx context.Context) (map[int64]int, error)
	// tag autocomplete
	SuggestTags(ctx context.Context, prefix string, limit int) ([]string, error)
	// timeline
	ListTitleRevisions(ctx context.Context, beatID int64) ([]domain.TitleRevision, error)
}

// GroupingEngine reassigns beats to groupings based on tag matching.
type GroupingEngine interface {
	Reassign(ctx context.Context, beatID int64) error
	ReassignAll(ctx context.Context, window time.Duration) error
	InvalidateCache()
}

// Scheduler interface for on-demand operations
type Scheduler interface {
	UpdateFeedNow(ctx context.Context, feedID int64) error
	ExtractContentNow(ctx context.Context, itemID int64) error
	UpdatePreferenceSummary(ctx context.Context) error
	TriggerPreferenceUpdate()
	ParseFeed(ctx context.Context, feedURL string) (*domain.ParsedFeed, error)
}

// ConfigProvider provides server configuration
type ConfigProvider interface {
	GetServerConfig() (listen string, timeout time.Duration)
	GetFullConfig() *config.Config // returns the full config struct for display
}

// SetGroupingEngine wires the assignment engine so grouping CRUD handlers can
// trigger ReassignAll and the beats handler can filter by group. May be called
// after New(); a nil engine means the grouping assignment feature is disabled.
func (s *Server) SetGroupingEngine(e GroupingEngine) {
	s.groupingEngine = e
}

// GetPageSize returns the configured page size for pagination
func (s *Server) GetPageSize() int {
	cfg := s.config.GetFullConfig()
	return cfg.Server.PageSize
}

// generatePageNumbers creates a slice of page numbers for pagination display
func generatePageNumbers(currentPage, totalPages int) []int {
	if totalPages <= 0 {
		return []int{}
	}

	var pages []int

	// show up to 5 page numbers centered around current page
	start := currentPage - 2
	end := currentPage + 2

	// adjust bounds
	if start < 1 {
		start = 1
		end = start + 4
	}
	if end > totalPages {
		end = totalPages
		start = end - 4
		if start < 1 {
			start = 1
		}
	}

	for i := start; i <= end; i++ {
		pages = append(pages, i)
	}

	return pages
}

// New initializes a new server instance
func New(cfg ConfigProvider, database Database, scheduler Scheduler, version string, debug bool) *Server {
	// big-tag cache is created before funcMap so the isBigTag closure can reference it.
	// zero-valued expires ensures the first request triggers a refresh.
	cache := &bigTagsCache{
		tags: map[string]int{},
	}

	// create bluemonday policy for HTML sanitization
	htmlPolicy := bluemonday.UGCPolicy()
	// allow additional safe elements that might be in article content
	htmlPolicy.AllowAttrs("class").OnElements("div", "span", "p", "code", "pre")
	htmlPolicy.AllowElements("figure", "figcaption", "blockquote", "cite")

	// preserve whitespace in pre and code blocks for proper formatting
	htmlPolicy.AllowAttrs("style").OnElements("pre", "code")
	htmlPolicy.RequireParseableURLs(false) // allow data: URLs for syntax highlighting

	// template functions
	funcMap := template.FuncMap{
		"mul": func(a, b float64) float64 {
			return a * b
		},
		"add": func(a, b int) int {
			return a + b
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"isImageURL": func(s string) bool {
			lower := strings.ToLower(s)
			return strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
				strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") ||
				strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".webp") ||
				strings.HasSuffix(lower, ".ico")
		},
		"hasPrefix": strings.HasPrefix,
		"durationMinutes": func(d time.Duration) int {
			return int(d.Minutes())
		},
		"printf": fmt.Sprintf,
		"stripHTML": func(s string) string {
			// add spaces for common block elements before stripping to prevent words from running together
			s = html.UnescapeString(s)
			blockRe := regexp.MustCompile(`(?i)</?(p|br|div|li|h[1-6]|td|tr|table|blockquote)[^>]*>`)
			s = blockRe.ReplaceAllString(s, " ")
			// strip the remaining tags
			s = bluemonday.StrictPolicy().Sanitize(s)
			// collapse multiple spaces
			return strings.Join(strings.Fields(s), " ")
		},
		"unescapeHTML": html.UnescapeString,
		"safeHTML": func(s string) template.HTML {
			// fix common content extraction issues before sanitization

			// 1. handle double code tags - these should be pre+code blocks
			codeBlockRe := regexp.MustCompile(`<code><code>([\s\S]*?)</code></code>`)
			s = codeBlockRe.ReplaceAllString(s, "<pre><code>$1</code></pre>")

			// 2. fix standalone multi-line code blocks that should be in pre tags
			// look for code blocks that contain newlines or look like code (has braces, semicolons, etc)
			standaloneCodeRe := regexp.MustCompile(`<code>((?:[^<]*(?:[\n\r]|[{};])[^<]*)+)</code>`)
			s = standaloneCodeRe.ReplaceAllStringFunc(s, func(match string) string {
				// extract the content
				content := match[6 : len(match)-7] // remove <code> and </code>
				// check if it looks like a code block (has newlines or typical code syntax)
				if strings.Contains(content, "\n") ||
					(strings.Contains(content, "{") && strings.Contains(content, "}")) ||
					strings.Contains(content, ");") {
					return "<pre><code>" + content + "</code></pre>"
				}
				return match // leave inline code as is
			})

			// 3. ensure proper nesting - no code directly inside code
			s = strings.ReplaceAll(s, "<code><code>", "<code>")
			s = strings.ReplaceAll(s, "</code></code>", "</code>")

			// sanitize HTML content before rendering
			sanitized := htmlPolicy.Sanitize(s)
			return template.HTML(sanitized) //nolint:gosec // content is sanitized by bluemonday
		},
		"getDomain": func(urlStr string) string {
			u, err := url.Parse(urlStr)
			if err != nil {
				return ""
			}
			return u.Hostname()
		},
		"pathEscape": url.PathEscape,
		"extractImage": func(content, description string) string {
			imgRe := regexp.MustCompile(`(?i)<img[^>]+src="([^">]+)"`)
			if matches := imgRe.FindStringSubmatch(content); len(matches) > 1 {
				return matches[1]
			}
			if matches := imgRe.FindStringSubmatch(description); len(matches) > 1 {
				return matches[1]
			}
			return ""
		},
		"isBigTag": func(tag string) bool {
			cache.mu.RLock()
			defer cache.mu.RUnlock()
			return cache.tags[tag] > 0
		},
		"beatPrimaryTopic": func(b *domain.BeatWithMembers) string {
			cache.mu.RLock()
			defer cache.mu.RUnlock()
			return b.PrimaryTopicWithCounts(cache.tags)
		},
		"formatRelativeDay": func(t time.Time) string {
			now := time.Now()
			loc := now.Location()
			t = t.In(loc)
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			yesterday := today.AddDate(0, 0, -1)
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			switch {
			case !d.Before(today):
				return "今天 " + t.Format("15:04")
			case !d.Before(yesterday):
				return "昨天 " + t.Format("15:04")
			case now.Sub(t) < 7*24*time.Hour:
				return t.Format("Mon 15:04")
			default:
				return t.Format("Jan 2 15:04")
			}
		},
		"formatTime": func(t time.Time) string {
			return t.In(time.Now().Location()).Format("15:04")
		},
	}

	// parse component templates only
	templates := template.New("").Funcs(funcMap)

	// parse component templates that can be reused
	templates, err := templates.ParseFS(templateFS,
		"templates/article-card.html",
		"templates/beat-card.html",
		"templates/feed-card.html",
		"templates/article-content.html",
		"templates/pagination.html",
		"templates/topic-tags.html",
		"templates/topic-dropdowns.html",
		"templates/controls.html",
		"templates/preference-summary.html",
		"templates/feed-preview.html",
		"templates/groupings-list.html")
	if err != nil {
		log.Printf("[WARN] failed to parse templates: %v", err)
	}

	// parse page templates
	pageTemplates := make(map[string]*template.Template)
	pageNames := []string{"articles.html", "feeds.html", "settings.html", "rss-help.html", "source.html", "rsshub-explorer.html", "beats.html", "beat-detail.html", "groupings.html"}

	for _, pageName := range pageNames {
		tmpl := template.New("").Funcs(funcMap)
		tmpl, err = tmpl.ParseFS(templateFS,
			"templates/base.html",
			"templates/"+pageName,
			"templates/article-card.html",
			"templates/beat-card.html",
			"templates/feed-card.html",
			"templates/pagination.html",
			"templates/groupings-list.html")
		if err != nil {
			log.Printf("[WARN] failed to parse %s: %v", pageName, err)
			continue
		}
		pageTemplates[pageName] = tmpl
	}

	s := &Server{
		config:        cfg,
		db:            database,
		scheduler:     scheduler,
		version:       version,
		debug:         debug,
		router:        routegroup.New(http.NewServeMux()),
		templates:     templates,
		pageTemplates: pageTemplates,
		bigTags:       cache,
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// refreshBigTags refreshes the big-tags cache if it has expired (TTL: 5 minutes).
// no-op when bigTags is nil (server created outside New, e.g. in tests).
func (s *Server) refreshBigTags(ctx context.Context) {
	if s.bigTags == nil {
		return
	}
	s.bigTags.mu.RLock()
	expired := time.Now().After(s.bigTags.expires)
	s.bigTags.mu.RUnlock()
	if !expired {
		return
	}

	counts, err := s.db.GetBigTags(ctx, 5)
	if err != nil {
		log.Printf("[WARN] failed to refresh big tags cache: %v", err)
		return
	}

	s.bigTags.mu.Lock()
	s.bigTags.tags = counts
	s.bigTags.expires = time.Now().Add(5 * time.Minute)
	s.bigTags.mu.Unlock()
}

// Run starts the HTTP server and handles graceful shutdown
func (s *Server) Run(ctx context.Context) error {
	listen, timeout := s.config.GetServerConfig()
	log.Printf("[INFO] starting server on %s", listen)

	httpServer := &http.Server{
		Addr:         listen,
		Handler:      s.router,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	}

	go func() {
		<-ctx.Done()
		log.Printf("[INFO] shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("[WARN] server shutdown error: %v", err)
		}
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server error: %w", err)
	}

	return nil
}

// setupMiddleware configures standard middleware for the server
func (s *Server) setupMiddleware() {
	s.router.Use(rest.AppInfo("newscope", "umputun", s.version))
	s.router.Use(rest.Ping)

	if s.debug {
		s.router.Use(logger.New(logger.Log(lgr.Default()), logger.Prefix("[DEBUG]")).Handler)
	}

	s.router.Use(rest.Recoverer(lgr.Default()))
	s.router.Use(rest.Throttle(defaultThrottleLimit))
	s.router.Use(rest.SizeLimit(defaultSizeLimit))
}

// respondWithError logs an error and sends an HTTP error response
func (s *Server) respondWithError(w http.ResponseWriter, code int, message string, err error) {
	if err != nil {
		log.Printf("[WARN] %s: %v", message, err)
	}
	http.Error(w, message, code)
}

// setupRoutes configures application routes
func (s *Server) setupRoutes() {
	// serve static files using embedded filesystem
	fsys, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("[ERROR] can't create embedded file server: %v", err)
	}
	s.router.HandleFiles("/static", http.FS(fsys))

	// web UI routes
	cfg := s.config.GetFullConfig()
	if cfg != nil && features.BeatsEnabled(*cfg) {
		s.router.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/beats", http.StatusTemporaryRedirect)
		})
		s.router.HandleFunc("GET /beats", s.beatsHandler)
		s.router.HandleFunc("GET /beats/{id}", s.beatDetailHandler)
	} else {
		s.router.HandleFunc("GET /", s.articlesHandler)
	}
	s.router.HandleFunc("GET /articles", s.articlesHandler)
	s.router.HandleFunc("GET /search", s.searchHandler)
	s.router.HandleFunc("GET /source/{name}", s.sourceHandler)
	s.router.HandleFunc("GET /feeds", s.feedsHandler)
	s.router.HandleFunc("GET /feeds/rsshub", s.rsshubExplorerHandler)
	s.router.HandleFunc("GET /settings", s.settingsHandler)
	s.router.HandleFunc("GET /settings/groupings", s.groupingsSettingsHandler)
	s.router.HandleFunc("GET /settings/groupings/{id}/edit", s.groupingEditFormHandler)
	s.router.HandleFunc("GET /rss-help", s.rssHelpHandler)
	s.router.HandleFunc("GET /api/v1/rss-builder", s.rssBuilderHandler)

	// API routes
	s.router.Mount("/api/v1").Route(func(r *routegroup.Bundle) {
		r.HandleFunc("GET /status", s.statusHandler)
		r.HandleFunc("POST /feedback/{id}/{action}", s.feedbackHandler)
		r.HandleFunc("POST /extract/{id}", s.extractHandler)
		r.HandleFunc("GET /articles/{id}/content", s.articleContentHandler)
		r.HandleFunc("GET /articles/{id}/hide", s.hideContentHandler)

		// groupings
		r.HandleFunc("POST /groupings", s.createGroupingHandler)
		r.HandleFunc("PUT /groupings/{id}", s.updateGroupingHandler)
		r.HandleFunc("DELETE /groupings/{id}", s.deleteGroupingHandler)
		r.HandleFunc("POST /groupings/reorder", s.reorderGroupingsHandler)
		r.HandleFunc("GET /tags/suggest", s.suggestTagsHandler)

		// beats
		r.HandleFunc("GET /beats/search", s.beatSearchHandler)
		r.HandleFunc("POST /beats/{id}/feedback", s.beatFeedbackHandler)
		r.HandleFunc("POST /beats/{id}/view", s.beatViewHandler)

		// feed management
		r.HandleFunc("POST /feeds", s.createFeedHandler)
		r.HandleFunc("PUT /feeds/{id}", s.updateFeedHandler)
		r.HandleFunc("POST /feeds/{id}/enable", s.enableFeedHandler)
		r.HandleFunc("POST /feeds/{id}/disable", s.disableFeedHandler)
		r.HandleFunc("POST /feeds/{id}/fetch", s.fetchFeedHandler)
		r.HandleFunc("DELETE /feeds/{id}", s.deleteFeedHandler)

		// topic preferences management
		r.HandleFunc("POST /topics", s.addTopicHandler)
		r.HandleFunc("DELETE /topics/{topic}", s.deleteTopicHandler)

		// preference summary management (JSON API)
		r.HandleFunc("GET /preferences", s.getPreferencesHandler)
		r.HandleFunc("PUT /preferences", s.updatePreferencesHandler)
		r.HandleFunc("DELETE /preferences", s.deletePreferencesHandler)

		// preference summary management (HTMX handlers)
		r.HandleFunc("GET /preferences/view", s.preferenceViewHandler)
		r.HandleFunc("GET /preferences/edit", s.preferenceEditHandler)
		r.HandleFunc("POST /preferences/save", s.preferenceSaveHandler)
		r.HandleFunc("DELETE /preferences/reset", s.preferenceResetHandler)
		r.HandleFunc("POST /preferences/toggle", s.preferenceToggleHandler)

		// rSSHub integration
		r.HandleFunc("GET /rsshub/radar/", s.radarProxyHandler)
		r.HandleFunc("GET /rsshub/categories", s.rsshubCategoriesHandler)
		r.HandleFunc("GET /rsshub/namespaces", s.rsshubNamespacesHandler)
		r.HandleFunc("GET /rsshub/namespaces/{name}", s.rsshubNamespaceDetailHandler)
		r.HandleFunc("GET /rsshub/preview", s.rsshubPreviewHandler)
	})

	// RSS routes
	s.router.HandleFunc("GET /rss/{topic}", s.rssHandler)
	s.router.HandleFunc("GET /rss", s.rssHandler)
}
