package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/temikus/butter/internal/provider"
)

// defaultModelMap maps Anthropic model IDs to Bedrock model IDs.
// Users can override or extend via the model_map config field.
var defaultModelMap = map[string]string{
	"claude-opus-4-20250514":     "anthropic.claude-opus-4-20250514-v1:0",
	"claude-sonnet-4-20250514":   "anthropic.claude-sonnet-4-20250514-v1:0",
	"claude-3-5-sonnet-20241022": "anthropic.claude-3-5-sonnet-20241022-v2:0",
	"claude-3-5-sonnet-20240620": "anthropic.claude-3-5-sonnet-20240620-v1:0",
	"claude-3-5-haiku-20241022":  "anthropic.claude-3-5-haiku-20241022-v1:0",
	"claude-3-opus-20240229":     "anthropic.claude-3-opus-20240229-v1:0",
	"claude-3-sonnet-20240229":   "anthropic.claude-3-sonnet-20240229-v1:0",
	"claude-3-haiku-20240307":    "anthropic.claude-3-haiku-20240307-v1:0",
}

// Provider implements the Bedrock provider for Anthropic Claude models.
type Provider struct {
	client   *bedrockruntime.Client
	modelMap map[string]string
}

// New creates a new Bedrock provider with the given AWS config and optional
// model ID overrides. The awsCfg should be loaded via config.LoadDefaultConfig
// with the appropriate region and credentials.
func New(awsCfg aws.Config, modelOverrides map[string]string) *Provider {
	// Merge default map with overrides (overrides take precedence).
	merged := make(map[string]string, len(defaultModelMap)+len(modelOverrides))
	for k, v := range defaultModelMap {
		merged[k] = v
	}
	for k, v := range modelOverrides {
		merged[k] = v
	}

	return &Provider{
		client:   bedrockruntime.NewFromConfig(awsCfg),
		modelMap: merged,
	}
}

func (p *Provider) Name() string { return "bedrock" }

// SupportsOperation returns false for all standard operations. Bedrock is
// discovered as an Anthropic-native failover target via the AnthropicNativeHandler
// interface, not through the standard dispatch path.
func (p *Provider) SupportsOperation(_ provider.Operation) bool {
	return false
}

// MapModel converts an Anthropic model ID to a Bedrock model ID.
// Checks the explicit model map first, then falls back to a convention-based
// mapping (anthropic.{model}-v1:0).
func (p *Provider) MapModel(model string) string {
	if mapped, ok := p.modelMap[model]; ok {
		return mapped
	}
	// Convention fallback: prepend anthropic. prefix and append -v1:0
	return "anthropic." + model + "-v1:0"
}

// HandleAnthropicNative processes an Anthropic Messages API request by forwarding
// it to Bedrock's InvokeModel (or InvokeModelWithResponseStream for streaming).
// The response is returned in Anthropic Messages API format. For streaming,
// the response body is SSE (text/event-stream) converted from Bedrock's
// event-stream binary framing.
func (p *Provider) HandleAnthropicNative(ctx context.Context, body []byte, _ http.Header) (*http.Response, error) {
	// Extract model and stream from the request body.
	var partial struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &partial); err != nil {
		return nil, fmt.Errorf("parsing request body: %w", err)
	}

	bedrockModel := p.MapModel(partial.Model)

	if partial.Stream {
		return p.invokeStream(ctx, bedrockModel, body)
	}
	return p.invoke(ctx, bedrockModel, body)
}

// invoke handles non-streaming InvokeModel calls.
func (p *Provider) invoke(ctx context.Context, modelID string, body []byte) (*http.Response, error) {
	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(modelID),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return nil, &provider.ProviderError{
			StatusCode: errorStatusCode(err),
			Message:    err.Error(),
		}
	}

	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(output.Body)),
	}, nil
}

// invokeStream handles streaming InvokeModelWithResponseStream calls.
// It converts Bedrock's event-stream binary framing to SSE text format.
func (p *Provider) invokeStream(ctx context.Context, modelID string, body []byte) (*http.Response, error) {
	output, err := p.client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     aws.String(modelID),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return nil, &provider.ProviderError{
			StatusCode: errorStatusCode(err),
			Message:    err.Error(),
		}
	}

	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		events := output.GetStream().Events()
		for {
			select {
			case <-ctx.Done():
				_ = output.GetStream().Close()
				pw.CloseWithError(ctx.Err())
				return
			case event, ok := <-events:
				if !ok {
					// Channel closed — check for stream errors.
					if err := output.GetStream().Err(); err != nil {
						pw.CloseWithError(err)
					}
					return
				}
				switch v := event.(type) {
				case *types.ResponseStreamMemberChunk:
					// Each chunk contains an Anthropic streaming event JSON.
					// Extract the event type and re-emit as SSE text.
					var evt struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(v.Value.Bytes, &evt); err != nil {
						pw.CloseWithError(fmt.Errorf("parsing event type: %w", err))
						return
					}
					_, err := fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", evt.Type, string(v.Value.Bytes))
					if err != nil {
						pw.CloseWithError(err)
						return
					}
				}
			}
		}
	}()

	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}, nil
}

// ChatCompletion is not supported in Phase 2 (Bedrock only serves as an
// Anthropic-native failover target via HandleAnthropicNative).
func (p *Provider) ChatCompletion(_ context.Context, _ *provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, fmt.Errorf("bedrock: ChatCompletion not implemented; use HandleAnthropicNative")
}

func (p *Provider) ChatCompletionStream(_ context.Context, _ *provider.ChatRequest) (provider.Stream, error) {
	return nil, fmt.Errorf("bedrock: ChatCompletionStream not implemented; use HandleAnthropicNative")
}

func (p *Provider) Passthrough(_ context.Context, _, _ string, _ io.Reader, _ http.Header) (*http.Response, error) {
	return nil, fmt.Errorf("bedrock: Passthrough not implemented; use HandleAnthropicNative")
}

// errorStatusCode extracts an HTTP status code from an AWS SDK error.
// Returns 502 as a generic proxy error if the status code can't be determined.
func errorStatusCode(err error) int {
	// AWS SDK errors implement smithy.APIError wrapping an HTTP response.
	type httpErr interface {
		HTTPStatusCode() int
	}
	var he httpErr
	if errors.As(err, &he) {
		return he.HTTPStatusCode()
	}
	return 502
}
