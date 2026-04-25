package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/server/mocks"
)

func testGroupingServer(t *testing.T, db *mocks.DatabaseMock) *Server {
	t.Helper()
	cfg := &mocks.ConfigProviderMock{
		GetServerConfigFunc: func() (string, time.Duration) { return ":8080", 30 * time.Second },
		GetFullConfigFunc:   func() *config.Config { return &config.Config{} },
	}
	stubBigTags(db)
	return New(cfg, db, &mocks.SchedulerMock{}, "test", false)
}

func baseGroupingDB() *mocks.DatabaseMock {
	return &mocks.DatabaseMock{
		ListGroupingsFunc: func(ctx context.Context) ([]domain.Grouping, error) {
			return []domain.Grouping{
				{ID: 1, Name: "AI News", Slug: "ai-news", Tags: []string{"ai", "llm"}, DisplayOrder: 0},
				{ID: 2, Name: "Security", Slug: "security", Tags: []string{"security"}, DisplayOrder: 1},
			}, nil
		},
		GetTopicsFilteredFunc: func(ctx context.Context, minScore float64) ([]string, error) {
			return []string{"ai", "tech"}, nil
		},
		GetActiveFeedNamesFunc: func(ctx context.Context, minScore float64) ([]string, error) {
			return []string{"Feed A"}, nil
		},
	}
}

func TestGroupingsSettingsHandler(t *testing.T) {
	db := baseGroupingDB()
	srv := testGroupingServer(t, db)

	req := httptest.NewRequest("GET", "/settings/groupings", http.NoBody)
	w := httptest.NewRecorder()
	srv.groupingsSettingsHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "AI News")
	assert.Contains(t, body, "Security")
}

func TestCreateGroupingHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := baseGroupingDB()
		db.CreateGroupingFunc = func(ctx context.Context, g domain.Grouping) (int64, error) {
			assert.Equal(t, "Science", g.Name)
			assert.Equal(t, []string{"biology", "physics"}, g.Tags)
			return 3, nil
		}
		srv := testGroupingServer(t, db)

		form := url.Values{"name": {"Science"}, "tags": {"biology, physics"}}
		req := httptest.NewRequest("POST", "/api/v1/groupings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.createGroupingHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "AI News") // list re-rendered
	})

	t.Run("missing name", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		form := url.Values{"tags": {"ai"}}
		req := httptest.NewRequest("POST", "/api/v1/groupings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.createGroupingHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestUpdateGroupingHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := baseGroupingDB()
		db.UpdateGroupingFunc = func(ctx context.Context, g domain.Grouping) error {
			assert.Equal(t, int64(1), g.ID)
			assert.Equal(t, "AI Updated", g.Name)
			return nil
		}
		srv := testGroupingServer(t, db)

		form := url.Values{"name": {"AI Updated"}, "tags": {"ai"}}
		req := httptest.NewRequest("PUT", "/api/v1/groupings/1", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		srv.updateGroupingHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("invalid id", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		form := url.Values{"name": {"X"}}
		req := httptest.NewRequest("PUT", "/api/v1/groupings/bad", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "bad")
		w := httptest.NewRecorder()
		srv.updateGroupingHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing name", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		form := url.Values{"tags": {"ai"}}
		req := httptest.NewRequest("PUT", "/api/v1/groupings/1", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		srv.updateGroupingHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestDeleteGroupingHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		deleted := false
		db := baseGroupingDB()
		db.DeleteGroupingFunc = func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(1), id)
			deleted = true
			return nil
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("DELETE", "/api/v1/groupings/1", http.NoBody)
		req.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		srv.deleteGroupingHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, deleted)
	})

	t.Run("invalid id", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("DELETE", "/api/v1/groupings/bad", http.NoBody)
		req.SetPathValue("id", "bad")
		w := httptest.NewRecorder()
		srv.deleteGroupingHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestReorderGroupingsHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := baseGroupingDB()
		var gotIDs []int64
		db.ReorderGroupingsFunc = func(ctx context.Context, idsInOrder []int64) error {
			gotIDs = idsInOrder
			return nil
		}
		srv := testGroupingServer(t, db)

		body, _ := json.Marshal(map[string][]int64{"ids": {2, 1}})
		req := httptest.NewRequest("POST", "/api/v1/groupings/reorder", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.reorderGroupingsHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, []int64{2, 1}, gotIDs)
	})

	t.Run("invalid json", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("POST", "/api/v1/groupings/reorder", strings.NewReader("not-json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.reorderGroupingsHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestGroupingEditFormHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := baseGroupingDB()
		db.GetGroupingFunc = func(ctx context.Context, id int64) (domain.Grouping, error) {
			require.Equal(t, int64(1), id)
			return domain.Grouping{ID: 1, Name: "AI News", Slug: "ai-news", Tags: []string{"ai", "llm"}}, nil
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/settings/groupings/1/edit", http.NoBody)
		req.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		srv.groupingEditFormHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "AI News")
	})

	t.Run("invalid id", func(t *testing.T) {
		db := baseGroupingDB()
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/settings/groupings/bad/edit", http.NoBody)
		req.SetPathValue("id", "bad")
		w := httptest.NewRecorder()
		srv.groupingEditFormHandler(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestCreateGroupingHandler_TriggersReassign(t *testing.T) {
	db := baseGroupingDB()
	reassignCalled := make(chan struct{}, 1)

	db.CreateGroupingFunc = func(ctx context.Context, g domain.Grouping) (int64, error) {
		return 3, nil
	}

	srv := testGroupingServer(t, db)
	// inject a mock engine
	srv.groupingEngine = &mockGroupingEngine{
		reassignAllFn: func(ctx context.Context, window time.Duration) error {
			reassignCalled <- struct{}{}
			return nil
		},
	}

	form := url.Values{"name": {"New Group"}, "tags": {"ai"}}
	req := httptest.NewRequest("POST", "/api/v1/groupings",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	srv.createGroupingHandler(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// wait for goroutine
	select {
	case <-reassignCalled:
	case <-time.After(500 * time.Millisecond):
		t.Error("ReassignAll was not called after CreateGrouping")
	}
}

// mockGroupingEngine satisfies GroupingEngine for tests.
type mockGroupingEngine struct {
	reassignFn    func(ctx context.Context, beatID int64) error
	reassignAllFn func(ctx context.Context, window time.Duration) error
}

func (m *mockGroupingEngine) Reassign(ctx context.Context, beatID int64) error {
	if m.reassignFn != nil {
		return m.reassignFn(ctx, beatID)
	}
	return nil
}

func (m *mockGroupingEngine) ReassignAll(ctx context.Context, window time.Duration) error {
	if m.reassignAllFn != nil {
		return m.reassignAllFn(ctx, window)
	}
	return nil
}

func (m *mockGroupingEngine) InvalidateCache() {}

func TestSuggestTagsHandler(t *testing.T) {
	t.Run("returns matching options", func(t *testing.T) {
		db := baseGroupingDB()
		db.SuggestTagsFunc = func(ctx context.Context, prefix string, limit int) ([]string, error) {
			assert.Equal(t, "an", prefix)
			return []string{"anthropic", "android"}, nil
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/api/v1/tags/suggest?q=an", http.NoBody)
		w := httptest.NewRecorder()
		srv.suggestTagsHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, `<option value="anthropic">`)
		assert.Contains(t, body, `<option value="android">`)
	})

	t.Run("empty query returns options", func(t *testing.T) {
		db := baseGroupingDB()
		db.SuggestTagsFunc = func(ctx context.Context, prefix string, limit int) ([]string, error) {
			assert.Empty(t, prefix)
			return []string{"ai", "llm"}, nil
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/api/v1/tags/suggest", http.NoBody)
		w := httptest.NewRecorder()
		srv.suggestTagsHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `<option value="ai">`)
	})

	t.Run("db error returns empty", func(t *testing.T) {
		db := baseGroupingDB()
		db.SuggestTagsFunc = func(ctx context.Context, prefix string, limit int) ([]string, error) {
			return nil, fmt.Errorf("db error")
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/api/v1/tags/suggest?q=x", http.NoBody)
		w := httptest.NewRecorder()
		srv.suggestTagsHandler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Body.String())
	})

	t.Run("html-escapes special chars", func(t *testing.T) {
		db := baseGroupingDB()
		db.SuggestTagsFunc = func(ctx context.Context, prefix string, limit int) ([]string, error) {
			return []string{`</option><script>alert(1)</script>`}, nil
		}
		srv := testGroupingServer(t, db)

		req := httptest.NewRequest("GET", "/api/v1/tags/suggest?q=x", http.NoBody)
		w := httptest.NewRecorder()
		srv.suggestTagsHandler(w, req)

		body := w.Body.String()
		assert.NotContains(t, body, "<script>")
		assert.Contains(t, body, "&lt;script&gt;")
	})
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"empty string", "", []string{}},
		{"single tag", "ai", []string{"ai"}},
		{"comma separated", "ai, llm, tech", []string{"ai", "llm", "tech"}},
		{"with spaces", "  ai  , llm  ", []string{"ai", "llm"}},
		{"trailing comma", "ai,", []string{"ai"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTags(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}
