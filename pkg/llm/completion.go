package llm

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// chatCompletion dispatches to streaming or non-streaming based on useStreaming.
// Streaming mode is required by some providers (e.g. ChatGPT subscription via litellm)
// whose non-streaming response path is broken. The streamed deltas are accumulated
// into a ChatCompletionResponse with the same shape as the non-streaming API returns.
func chatCompletion(ctx context.Context, client *openai.Client, req openai.ChatCompletionRequest, useStreaming bool) (openai.ChatCompletionResponse, error) {
	if !useStreaming {
		return client.CreateChatCompletion(ctx, req)
	}

	req.Stream = true
	req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	stream, err := client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer stream.Close()

	var content, reasoning strings.Builder
	var role, finishReason string
	resp := openai.ChatCompletionResponse{Object: "chat.completion"}
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return openai.ChatCompletionResponse{}, recvErr
		}
		if resp.ID == "" {
			resp.ID = chunk.ID
			resp.Created = chunk.Created
			resp.Model = chunk.Model
		}
		if chunk.Usage != nil {
			resp.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Role != "" {
			role = delta.Role
		}
		content.WriteString(delta.Content)
		reasoning.WriteString(delta.ReasoningContent)
		if chunk.Choices[0].FinishReason != "" {
			finishReason = string(chunk.Choices[0].FinishReason)
		}
	}

	if role == "" {
		role = openai.ChatMessageRoleAssistant
	}
	resp.Choices = []openai.ChatCompletionChoice{{
		Index: 0,
		Message: openai.ChatCompletionMessage{
			Role:             role,
			Content:          content.String(),
			ReasoningContent: reasoning.String(),
		},
		FinishReason: openai.FinishReason(finishReason),
	}}
	return resp, nil
}
