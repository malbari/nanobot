package completions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nanobot-ai/nanobot/pkg/complete"
	"github.com/nanobot-ai/nanobot/pkg/llm/progress"
	"github.com/nanobot-ai/nanobot/pkg/log"
	"github.com/nanobot-ai/nanobot/pkg/mcp"
	"github.com/nanobot-ai/nanobot/pkg/types"
)

type Client struct {
	Config
}

type Config struct {
	APIKey  string
	BaseURL string
	Headers map[string]string
}

// NewClient creates a new OpenAI Chat Completions client with the provided API key and base URL.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if _, ok := cfg.Headers["Authorization"]; !ok && cfg.APIKey != "" {
		cfg.Headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	if _, ok := cfg.Headers["Content-Type"]; !ok {
		cfg.Headers["Content-Type"] = "application/json"
	}

	return &Client{
		Config: cfg,
	}
}

func (c *Client) Complete(ctx context.Context, completionRequest types.CompletionRequest, opts ...types.CompletionOptions) (*types.CompletionResponse, error) {
	req, err := toRequest(&completionRequest)
	if err != nil {
		return nil, err
	}

	ts := time.Now()
	resp, err := c.complete(ctx, completionRequest.Agent, req, opts...)
	if err != nil {
		return nil, err
	}

	return toResponse(resp, ts)
}

func (c *Client) complete(ctx context.Context, agentName string, req Request, opts ...types.CompletionOptions) (*Response, error) {
	var (
		opt = complete.Complete(opts...)
	)

	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	data, _ := json.Marshal(req)
	log.Messages(ctx, "completions-api", true, data)

	// Build the URL with api-version if AZURE_OPENAI_API_VERSION is defined
    apiVersion := os.Getenv("AZURE_OPENAI_API_VERSION")
    url := c.BaseURL + "/chat/completions"
    if apiVersion != "" {
	    url = url + "?api-version=" + apiVersion
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(data))
    if err != nil {
	    return nil, err
    }
	// Log the URL used
    log.Infof(ctx, "OpenAI Chat Completions URL: %s", httpReq.URL.String())
	
	for key, value := range c.Headers {
		httpReq.Header.Set(key, value)
	}

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("failed to get response from OpenAI Chat Completions API: %s %q", httpResp.Status, string(body))
	}

	// Peek first bytes to detect if it's SSE or complete JSON
	reader := bufio.NewReader(httpResp.Body)
	firstBytes, err := reader.Peek(20)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to peek response: %w", err)
	}

	// Check if response is in SSE format (starts with "data: ") or complete JSON (starts with "{")
	isSSE := strings.HasPrefix(strings.TrimSpace(string(firstBytes)), "data: ")

	if !isSSE {
		// Azure OpenAI returns complete JSON response (no streaming)
		bodyBytes, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		var resp Response
		if err := json.Unmarshal(bodyBytes, &resp); err != nil {
			return nil, fmt.Errorf("failed to decode complete response: %w", err)
		}

		log.Messages(ctx, "completions-api", false, bodyBytes)

		// Generate ID if empty (Azure returns empty ID)
		if resp.ID == "" {
			resp.ID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		}

		// Send progress for the complete response
		if opt.ProgressToken != nil && len(resp.Choices) > 0 {
			choice := resp.Choices[0]
			if choice.Message != nil && choice.Message.Content.Text != nil {
				progress.Send(ctx, &types.CompletionProgress{
					Model:     resp.Model,
					Agent:     agentName,
					MessageID: resp.ID,
					Item: types.CompletionItem{
						ID:      fmt.Sprintf("%s-content", resp.ID),
						Partial: false,
						HasMore: false,
						Content: &mcp.Content{
							Type: "text",
							Text: *choice.Message.Content.Text,
						},
					},
				}, opt.ProgressToken)
			}

			// Send progress for tool calls if any
			for i, toolCall := range choice.Message.ToolCalls {
				progress.Send(ctx, &types.CompletionProgress{
					Model:     resp.Model,
					Agent:     agentName,
					MessageID: resp.ID,
					Item: types.CompletionItem{
						ID:      fmt.Sprintf("%s-t-%d", resp.ID, i),
						Partial: false,
						HasMore: false,
						ToolCall: &types.ToolCall{
							CallID:    toolCall.ID,
							Name:      toolCall.Function.Name,
							Arguments: toolCall.Function.Arguments,
						},
					},
				}, opt.ProgressToken)
			}
		}

		return &resp, nil
	}

	// Handle SSE streaming format - process line by line in real-time
	var (
		lines       = bufio.NewScanner(reader)
		resp        Response
		initialized = false
		toolCalls   = make(map[int]*ToolCall)
	)

	for lines.Scan() {
		line := lines.Text()

		// Handle SSE format
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimSpace(data)

		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Errorf(ctx, "failed to decode streaming chunk: %v: %s", err, data)
			continue
		}

		// Initialize response from first chunk
		if !initialized {
			resp = Response{
				ID:                chunk.ID,
				Object:            "chat.completion",
				Created:           chunk.Created,
				Model:             chunk.Model,
				SystemFingerprint: chunk.SystemFingerprint,
				Choices:           []Choice{{Index: 0, Message: &Message{Role: "assistant"}}},
			}
			initialized = true
		}

		// Update ID if empty and chunk has ID (Azure sends ID in later chunks)
		if resp.ID == "" && chunk.ID != "" {
			resp.ID = chunk.ID
		}

		// Handle usage information
		if chunk.Usage != nil {
			resp.Usage = chunk.Usage
		}

		// Process choice deltas
		for _, choice := range chunk.Choices {
			if choice.Index >= len(resp.Choices) {
				continue
			}

			delta := choice.Delta
			
			// Azure OpenAI may send complete message instead of delta
			// Handle this case by checking if Message is present
			if delta == nil && choice.Message != nil {
				// Copy complete message to response
				resp.Choices[choice.Index].Message = choice.Message
				
				// Generate ID if empty
				if resp.ID == "" {
					resp.ID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
				}
				
				// Send progress for complete message
				if opt.ProgressToken != nil && choice.Message.Content.Text != nil {
					progress.Send(ctx, &types.CompletionProgress{
						Model:     resp.Model,
						Agent:     agentName,
						MessageID: resp.ID,
						Item: types.CompletionItem{
							ID:      fmt.Sprintf("%s-%d", resp.ID, choice.Index),
							Partial: false,
							HasMore: false,
							Content: &mcp.Content{
								Type: "text",
								Text: *choice.Message.Content.Text,
							},
						},
					}, opt.ProgressToken)
				}
				
				// Handle tool calls in complete message
				for i, toolCall := range choice.Message.ToolCalls {
					progress.Send(ctx, &types.CompletionProgress{
						Model:     resp.Model,
						Agent:     agentName,
						MessageID: resp.ID,
						Item: types.CompletionItem{
							ID:      fmt.Sprintf("%s-t-%d", resp.ID, i),
							Partial: false,
							HasMore: false,
							ToolCall: &types.ToolCall{
								CallID:    toolCall.ID,
								Name:      toolCall.Function.Name,
								Arguments: toolCall.Function.Arguments,
							},
						},
					}, opt.ProgressToken)
				}
				
				continue
			}
			
			if delta == nil {
				continue
			}

			// Determine if this is the final chunk for this choice
			isFinished := choice.FinishReason != nil

			// Handle role
			if delta.Role != "" && resp.Choices[choice.Index].Message != nil {
				resp.Choices[choice.Index].Message.Role = delta.Role
			}

			// Handle content
			if delta.Content != nil {
				if resp.Choices[choice.Index].Message.Content.Text == nil {
					resp.Choices[choice.Index].Message.Content.Text = new(string)
				}
				*resp.Choices[choice.Index].Message.Content.Text += *delta.Content

				// Only send progress if we have a valid message ID
				if resp.ID != "" && opt.ProgressToken != nil {
					progress.Send(ctx, &types.CompletionProgress{
						Model:     resp.Model,
						Agent:     agentName,
						MessageID: resp.ID,
						Item: types.CompletionItem{
							ID:      fmt.Sprintf("%s-%d", resp.ID, choice.Index),
							Partial: true,
							HasMore: !isFinished,
							Content: &mcp.Content{
								Type: "text",
								Text: *delta.Content,
							},
						},
					}, opt.ProgressToken)
				}
			}

			// Handle tool calls
			if delta.ToolCalls != nil {
				for i, toolCall := range delta.ToolCalls {
					index := i
					if toolCall.Index != nil {
						index = *toolCall.Index
					}
					if _, exists := toolCalls[index]; !exists {
						toolCalls[index] = &ToolCall{
							ID:   toolCall.ID,
							Type: toolCall.Type,
							Function: FunctionCall{
								Name:      toolCall.Function.Name,
								Arguments: toolCall.Function.Arguments,
							},
						}
					} else {
						// Append to existing tool call arguments
						toolCalls[index].Function.Arguments += toolCall.Function.Arguments
					}

					// Only send progress if we have a valid message ID
					if resp.ID != "" && opt.ProgressToken != nil {
						progress.Send(ctx, &types.CompletionProgress{
							Model:     resp.Model,
							Agent:     agentName,
							MessageID: resp.ID,
							Item: types.CompletionItem{
								ID:      fmt.Sprintf("%s-t-%d", resp.ID, index),
								Partial: true,
								HasMore: !isFinished,
								ToolCall: &types.ToolCall{
									CallID:    toolCalls[index].ID,
									Name:      toolCalls[index].Function.Name,
									Arguments: toolCall.Function.Arguments,
								},
							},
						}, opt.ProgressToken)
					}
				}
			}

			// Handle finish reason
			if choice.FinishReason != nil {
				resp.Choices[choice.Index].FinishReason = choice.FinishReason
			}

			// Handle refusal
			if delta.Refusal != nil {
				resp.Choices[choice.Index].Message.Refusal = delta.Refusal
			}
		}
	}

	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("failed to read streaming response: %w", err)
	}

	// Convert tool calls map to slice
	if len(toolCalls) > 0 {
		resp.Choices[0].Message.ToolCalls = make([]ToolCall, len(toolCalls))
		for i, toolCall := range toolCalls {
			resp.Choices[0].Message.ToolCalls[i] = *toolCall
		}
	}

	respData, err := json.Marshal(resp)
	if err == nil {
		log.Messages(ctx, "completions-api", false, respData)
	}

	return &resp, nil
}
