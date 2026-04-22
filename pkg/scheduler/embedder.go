package scheduler

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// OpenAIEmbedder computes embeddings using an OpenAI-compatible API.
type OpenAIEmbedder struct {
	client *openai.Client
	model  string
}

// NewOpenAIEmbedder creates an embedder backed by an OpenAI-compatible endpoint.
// If endpoint is empty the default OpenAI API URL is used.
func NewOpenAIEmbedder(apiKey, endpoint, model string) *OpenAIEmbedder {
	cfg := openai.DefaultConfig(apiKey)
	if endpoint != "" {
		cfg.BaseURL = endpoint
	}
	return &OpenAIEmbedder{
		client: openai.NewClientWithConfig(cfg),
		model:  model,
	}
}

// Embed returns the embedding vector for the given text.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequestStrings{
		Input: []string{text},
		Model: openai.EmbeddingModel(e.model),
	})
	if err != nil {
		return nil, fmt.Errorf("create embedding: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("create embedding: empty response")
	}
	return resp.Data[0].Embedding, nil
}
