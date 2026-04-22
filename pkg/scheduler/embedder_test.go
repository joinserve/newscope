package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIEmbedder_Embed(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "index": 0, "embedding": want},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]int{"prompt_tokens": 5, "total_tokens": 5},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper, error not actionable
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedder("test-key", srv.URL, "text-embedding-3-small")
	got, err := embedder.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOpenAIEmbedder_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"object": "list",
			"data":   []any{},
			"model":  "text-embedding-3-small",
			"usage":  map[string]int{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper, error not actionable
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedder("test-key", srv.URL, "text-embedding-3-small")
	_, err := embedder.Embed(context.Background(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func TestOpenAIEmbedder_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`)) //nolint:errcheck // test helper
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedder("test-key", srv.URL, "text-embedding-3-small")
	_, err := embedder.Embed(context.Background(), "hello")
	require.Error(t, err)
}

func TestNewOpenAIEmbedder_DefaultEndpoint(t *testing.T) {
	// empty endpoint should not panic; actual requests are not made in this test
	embedder := NewOpenAIEmbedder("key", "", "model")
	assert.NotNil(t, embedder)
}
