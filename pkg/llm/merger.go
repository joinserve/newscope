package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/go-pkgz/repeater/v2"
	"github.com/sashabaranov/go-openai"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
)

const mergerSystemPrompt = `You are an AI assistant that synthesizes groups of related news articles into a single canonical representation.
Given several articles all reporting the same news event, produce ONE canonical title and ONE canonical summary.

Rules:
- canonical_title: concise, factual headline (max 120 chars). Start directly with the subject — no meta-language.
- canonical_summary: comprehensive synthesis of the key facts, findings, and details (300-500 chars).
  Start DIRECTLY with the facts. Do NOT use meta-language like "The articles discuss", "This story covers", etc.
  Write as if you ARE presenting the information.

Examples of good canonical_summary:
- "Go 1.22 introduces range-over-function iterators and cuts compile times by 50% through parallel compilation. Toolchain versioning simplifies Go version management. Runtime garbage collection improvements reduce memory usage by 15%."
- "Scientists confirm extensive water ice deposits 3.7 km deep at the Martian equator using Mars Express radar data. Discovery challenges current climate models and could support future human missions."

Examples of BAD canonical_summary (never write like this):
- "The articles discuss the release of Go 1.22..." ❌
- "This story covers the discovery of water on Mars..." ❌
- "The group of articles reports on..." ❌

Respond with a JSON object: {"canonical_title": "...", "canonical_summary": "..."}`

// mergeResponse is the expected JSON structure from the LLM.
type mergeResponse struct {
	Title   string `json:"canonical_title"`
	Summary string `json:"canonical_summary"`
}

// defaultMergerForbiddenPrefixes mirrors the classifier defaults.
var defaultMergerForbiddenPrefixes = []string{
	"The articles discuss", "The articles cover", "The articles report",
	"The articles describe", "The articles examine", "The articles explore",
	"This story covers", "This story discusses", "This story reports",
	"The group of articles", "These articles", "This collection",
	"The article discusses", "The article covers", "The article reports",
	"This article", "This post", "The post", "The piece",
	"Discusses", "Covers", "Reports", "Describes", "Examines", "Explores",
}

// Merger produces a canonical title and summary for a beat from its member items.
// It reuses the same LLM endpoint and credentials as the article classifier.
type Merger struct {
	client *openai.Client
	config config.LLMConfig
}

// NewMerger creates a Merger using the same LLM config as the article classifier.
func NewMerger(cfg config.LLMConfig) *Merger {
	clientConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.Endpoint != "" {
		clientConfig.BaseURL = cfg.Endpoint
	}
	return &Merger{client: openai.NewClientWithConfig(clientConfig), config: cfg}
}

// Merge generates a canonical title and summary from the beat's member items.
// It retries when the LLM produces a summary starting with a forbidden prefix,
// and cleans the summary on the final attempt as a fallback.
func (m *Merger) Merge(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
	if len(members) == 0 {
		return domain.BeatCanonical{}, fmt.Errorf("no members to merge")
	}

	retryAttempts := m.config.Classification.SummaryRetryAttempts
	if retryAttempts == 0 {
		retryAttempts = 3
	}

	prompt := m.buildPrompt(members)
	var result domain.BeatCanonical

	for attempt := 0; attempt <= retryAttempts; attempt++ {
		err := repeater.NewBackoff(5, time.Second,
			repeater.WithMaxDelay(30*time.Second),
			repeater.WithJitter(0.1),
		).Do(ctx, func() error {
			req := openai.ChatCompletionRequest{
				Model: m.config.Model,
				Messages: []openai.ChatCompletionMessage{
					{Role: openai.ChatMessageRoleSystem, Content: mergerSystemPrompt},
					{Role: openai.ChatMessageRoleUser, Content: prompt},
				},
			}
			if isReasoningModel(m.config.Model) {
				req.MaxCompletionTokens = m.config.MaxTokens
			} else {
				req.MaxTokens = m.config.MaxTokens
				req.Temperature = float32(m.config.Temperature)
			}

			resp, err := m.client.CreateChatCompletion(ctx, req)
			if err != nil {
				return fmt.Errorf("llm request failed: %w", err)
			}
			if len(resp.Choices) == 0 {
				return fmt.Errorf("no response from llm")
			}

			parsed, parseErr := m.parseResponse(resp.Choices[0].Message.Content)
			if parseErr != nil {
				return fmt.Errorf("parse response: %w", parseErr)
			}
			result = parsed
			return nil
		})
		if err != nil {
			return domain.BeatCanonical{}, err
		}

		if !m.hasForbiddenPrefix(result.Summary) {
			if attempt > 0 {
				log.Printf("[INFO] merge_worker: summary ok after %d retries", attempt)
			}
			return result, nil
		}

		if attempt == retryAttempts {
			log.Printf("[WARN] merge_worker: exhausted %d retries, cleaning forbidden prefix", retryAttempts)
			result.Summary = m.cleanSummary(result.Summary)
			return result, nil
		}

		log.Printf("[INFO] merge_worker: retrying merge (attempt %d/%d): summary has forbidden prefix", attempt+1, retryAttempts)
		if attempt == 0 {
			prompt += "\n\nIMPORTANT: Write the canonical_summary DIRECTLY without meta-language. Do NOT start with 'The articles discuss' or similar phrases."
		}
	}

	return result, nil
}

// buildPrompt formats the member items into a prompt for the LLM.
func (m *Merger) buildPrompt(members []domain.ClassifiedItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Here are %d articles reporting the same news event. Synthesize them into one canonical title and summary.\n\n", len(members)))
	for i, item := range members {
		sb.WriteString(fmt.Sprintf("%d. Title: %s\n", i+1, item.Title))
		if s := item.GetSummary(); s != "" {
			sb.WriteString(fmt.Sprintf("   Summary: %s\n", s))
		}
		if topics := item.GetTopics(); len(topics) > 0 {
			sb.WriteString(fmt.Sprintf("   Topics: %s\n", strings.Join(topics, ", ")))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(`Respond with JSON: {"canonical_title": "...", "canonical_summary": "..."}`)
	return sb.String()
}

// parseResponse extracts and validates the canonical title and summary from the LLM JSON output.
func (m *Merger) parseResponse(content string) (domain.BeatCanonical, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || start >= end {
		return domain.BeatCanonical{}, fmt.Errorf("no json object in response")
	}

	var mr mergeResponse
	if err := json.Unmarshal([]byte(content[start:end+1]), &mr); err != nil {
		return domain.BeatCanonical{}, fmt.Errorf("unmarshal merge response: %w", err)
	}
	if strings.TrimSpace(mr.Title) == "" {
		return domain.BeatCanonical{}, fmt.Errorf("empty canonical_title in response")
	}
	if strings.TrimSpace(mr.Summary) == "" {
		return domain.BeatCanonical{}, fmt.Errorf("empty canonical_summary in response")
	}

	return domain.BeatCanonical{Title: mr.Title, Summary: mr.Summary}, nil
}

// getForbiddenPrefixes returns config-defined prefixes when set, otherwise the defaults.
// Mirrors Classifier.getForbiddenPrefixes (classifier.go).
func (m *Merger) getForbiddenPrefixes() []string {
	if len(m.config.Classification.ForbiddenSummaryPrefixes) > 0 {
		return m.config.Classification.ForbiddenSummaryPrefixes
	}
	return defaultMergerForbiddenPrefixes
}

// hasForbiddenPrefix reports whether summary starts with a forbidden phrase.
func (m *Merger) hasForbiddenPrefix(summary string) bool {
	lower := strings.ToLower(strings.TrimSpace(summary))
	for _, prefix := range m.getForbiddenPrefixes() {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

// cleanSummary strips a forbidden meta-language prefix from the canonical summary.
func (m *Merger) cleanSummary(summary string) string {
	lower := strings.ToLower(strings.TrimSpace(summary))
	for _, prefix := range m.getForbiddenPrefixes() {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			remaining := strings.TrimSpace(summary[len(prefix):])
			if remaining != "" {
				runes := []rune(remaining)
				runes[0] = unicode.ToUpper(runes[0])
				return string(runes)
			}
		}
	}
	return summary
}
