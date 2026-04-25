package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

func TestLLMEntityExtractor_Extract(t *testing.T) {
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "Claude 4.5 release", Summary: "Anthropic releases new model"}},
		{Item: &domain.Item{ID: 2, Title: "SpaceX launch", Summary: "Falcon 9 carries satellites"}},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		// verify system prompt is set
		assert.Equal(t, openai.ChatMessageRoleSystem, req.Messages[0].Role)
		assert.Contains(t, req.Messages[0].Content, "entity extractor")

		// verify user message contains item titles
		assert.Contains(t, req.Messages[1].Content, "Claude 4.5 release")
		assert.Contains(t, req.Messages[1].Content, "SpaceX launch")

		resp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{
					Content: `[["claude","anthropic"],["spacex","falcon 9"]]`,
				}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	extractor := NewLLMEntityExtractor("test-key", ts.URL, "gpt-4o-mini")
	result, err := extractor.Extract(context.Background(), items)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, []string{"claude", "anthropic"}, result[0])
	assert.Equal(t, []string{"spacex", "falcon 9"}, result[1])
}

func TestLLMEntityExtractor_Extract_LengthMismatch(t *testing.T) {
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "A", Summary: ""}},
		{Item: &domain.Item{ID: 2, Title: "B", Summary: ""}},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{
					Content: `[["claude"]]`, // only 1 result for 2 items
				}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	extractor := NewLLMEntityExtractor("test-key", ts.URL, "gpt-4o-mini")
	_, err := extractor.Extract(context.Background(), items)
	assert.ErrorContains(t, err, "length mismatch")
}

func TestLLMEntityExtractor_Extract_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: `not valid json`}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	extractor := NewLLMEntityExtractor("test-key", ts.URL, "gpt-4o-mini")
	items := []domain.ClassifiedItem{{Item: &domain.Item{ID: 1, Title: "T", Summary: ""}}}
	_, err := extractor.Extract(context.Background(), items)
	assert.ErrorContains(t, err, "parse entity response")
}
