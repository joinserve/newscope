package scheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sashabaranov/go-openai"

	"github.com/umputun/newscope/pkg/domain"
)

const entitySystemPrompt = `You are an entity extractor. For each article extract up to 5 named entities.
Allowed entity types: companies, products, public figures, locations.
Output a JSON array of arrays. Outer length must equal the number of input articles.
Each inner array contains lowercase entity strings.
Do NOT extract generic nouns (e.g. "AI", "company", "election", "technology").
When unsure, skip — an empty inner array is fine.`

// LLMEntityExtractor extracts named entities using an OpenAI-compatible chat API.
type LLMEntityExtractor struct {
	client *openai.Client
	model  string
}

// NewLLMEntityExtractor creates an extractor backed by an OpenAI-compatible endpoint.
// If endpoint is empty the default OpenAI API URL is used.
func NewLLMEntityExtractor(apiKey, endpoint, model string) *LLMEntityExtractor {
	cfg := openai.DefaultConfig(apiKey)
	if endpoint != "" {
		cfg.BaseURL = endpoint
	}
	return &LLMEntityExtractor{
		client: openai.NewClientWithConfig(cfg),
		model:  model,
	}
}

// Extract sends items to the LLM and returns the extracted entities per item.
func (e *LLMEntityExtractor) Extract(ctx context.Context, items []domain.ClassifiedItem) ([][]string, error) {
	type inputItem struct {
		I       int    `json:"i"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}

	input := make([]inputItem, len(items))
	for i, item := range items {
		input[i] = inputItem{I: i, Title: item.Title, Summary: item.Summary}
	}
	userMsg, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}

	resp, err := e.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: e.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: entitySystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: string(userMsg)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm entity extract: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("llm entity extract: empty response")
	}

	var result [][]string
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("parse entity response: %w", err)
	}
	if len(result) != len(items) {
		return nil, fmt.Errorf("entity response length mismatch: got %d, want %d", len(result), len(items))
	}
	return result, nil
}
