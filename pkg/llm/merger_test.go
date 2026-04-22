package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
)

func mergerTestServer(t *testing.T, responseContent string) (*httptest.Server, config.LLMConfig) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: responseContent}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	cfg := config.LLMConfig{
		Endpoint:    server.URL + "/v1",
		APIKey:      "test-key",
		Model:       "gpt-4o",
		Temperature: 0.3,
		MaxTokens:   500,
	}
	return server, cfg
}

func mergerMembers() []domain.ClassifiedItem {
	return []domain.ClassifiedItem{
		{
			Item: &domain.Item{Title: "Go 1.22 Released", Summary: "Go 1.22 brings range-over-function iterators."},
			Classification: &domain.Classification{
				Topics:  []string{"golang", "programming"},
				Summary: "Go 1.22 brings range-over-function iterators.",
			},
		},
		{
			Item: &domain.Item{Title: "Go 1.22 Announced", Summary: "The Go team announces Go 1.22 with performance improvements."},
			Classification: &domain.Classification{
				Topics:  []string{"golang", "backend"},
				Summary: "The Go team announces Go 1.22 with performance improvements.",
			},
		},
	}
}

func TestMerger_ContractOutputShape(t *testing.T) {
	responseJSON := `{"canonical_title": "Go 1.22 Released with Iterators and 50% Faster Compilation", "canonical_summary": "Go 1.22 introduces range-over-function iterators enabling cleaner iteration patterns. Compilation speeds improve by 50% through parallel compilation. New toolchain versioning simplifies managing Go versions."}`

	server, cfg := mergerTestServer(t, responseJSON)
	defer server.Close()

	m := NewMerger(cfg)
	result, err := m.Merge(context.Background(), mergerMembers())

	require.NoError(t, err)
	assert.NotEmpty(t, result.Title, "canonical_title must be non-empty")
	assert.NotEmpty(t, result.Summary, "canonical_summary must be non-empty")
	assert.Equal(t, "Go 1.22 Released with Iterators and 50% Faster Compilation", result.Title)
	assert.Equal(t, "Go 1.22 introduces range-over-function iterators enabling cleaner iteration patterns. Compilation speeds improve by 50% through parallel compilation. New toolchain versioning simplifies managing Go versions.", result.Summary)
}

func TestMerger_ForbiddenPrefixCleaned(t *testing.T) {
	responseJSON := `{"canonical_title": "Go 1.22 Release", "canonical_summary": "The articles discuss the release of Go 1.22 with range-over-function iterators and performance improvements."}`

	server, cfg := mergerTestServer(t, responseJSON)
	defer server.Close()

	m := NewMerger(cfg)
	result, err := m.Merge(context.Background(), mergerMembers())

	require.NoError(t, err)
	assert.NotEmpty(t, result.Title)
	assert.NotEmpty(t, result.Summary)
	// forbidden prefix must be stripped
	assert.False(t, m.hasForbiddenPrefix(result.Summary),
		"summary must not start with forbidden prefix, got: %q", result.Summary)
}

func TestMerger_EmptyMembers(t *testing.T) {
	m := NewMerger(config.LLMConfig{APIKey: "k", Model: "m"})
	_, err := m.Merge(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no members")
}

func TestMerger_EmptyTitleRejected(t *testing.T) {
	responseJSON := `{"canonical_title": "", "canonical_summary": "Some valid summary content here."}`

	server, cfg := mergerTestServer(t, responseJSON)
	defer server.Close()

	m := NewMerger(cfg)
	_, err := m.Merge(context.Background(), mergerMembers())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty canonical_title")
}

func TestMerger_EmptySummaryRejected(t *testing.T) {
	responseJSON := `{"canonical_title": "Valid Title Here", "canonical_summary": ""}`

	server, cfg := mergerTestServer(t, responseJSON)
	defer server.Close()

	m := NewMerger(cfg)
	_, err := m.Merge(context.Background(), mergerMembers())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty canonical_summary")
}

func TestMerger_MalformedJSONReturnsError(t *testing.T) {
	server, cfg := mergerTestServer(t, "not json at all")
	defer server.Close()

	m := NewMerger(cfg)
	_, err := m.Merge(context.Background(), mergerMembers())
	require.Error(t, err)
}

func TestMerger_BuildPromptIncludesTitlesAndSummaries(t *testing.T) {
	m := NewMerger(config.LLMConfig{APIKey: "k", Model: "m"})
	members := mergerMembers()
	prompt := m.buildPrompt(members)

	assert.Contains(t, prompt, "Go 1.22 Released")
	assert.Contains(t, prompt, "Go 1.22 Announced")
	assert.Contains(t, prompt, "range-over-function iterators")
	assert.Contains(t, prompt, "golang")
}
