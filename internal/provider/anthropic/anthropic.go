package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/temikus/butter/internal/provider"
)

const (
	defaultBaseURL    = "https://api.anthropic.com/v1"
	anthropicVersion  = "2023-06-01"
)

// Provider implements provider.Provider for the Anthropic Messages API.
type Provider struct {
	baseURL string
	client  *http.Client
	bufPool sync.Pool
}

// New creates an Anthropic provider. If baseURL is empty, the default is used.
func New(baseURL string, client *http.Client) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, 0, 4096)
				return &buf
			},
		},
	}
}

func (p *Provider) Name() string { return "anthropic" }

func (p *Provider) SupportsOperation(op provider.Operation) bool {
	switch op {
	case provider.OpChatCompletion, provider.OpChatCompletionStream, provider.OpPassthrough:
		return true
	}
	return false
}

// SetAuthHeader implements provider.AuthHeaderSetter for Anthropic's x-api-key auth.
func (p *Provider) SetAuthHeader(headers http.Header, apiKey string) {
	headers.Set("x-api-key", apiKey)
	headers.Set("anthropic-version", anthropicVersion)
}

func (p *Provider) ChatCompletion(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	body, err := translateRequest(req.RawBody)
	if err != nil {
		return nil, fmt.Errorf("translating request: %w", err)
	}

	httpReq, err := p.buildRequest(ctx, body, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		msg := extractErrorMessage(respBody)
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}

	translated, err := translateResponse(respBody)
	if err != nil {
		return nil, fmt.Errorf("translating response: %w", err)
	}

	return &provider.ChatResponse{
		RawBody:    translated,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}, nil
}

func (p *Provider) ChatCompletionStream(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
	body, err := translateRequest(req.RawBody)
	if err != nil {
		return nil, fmt.Errorf("translating request: %w", err)
	}

	httpReq, err := p.buildRequest(ctx, body, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		msg := extractErrorMessage(respBody)
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}

	return &anthropicStream{
		reader: bufio.NewReaderSize(resp.Body, 4096),
		body:   resp.Body,
	}, nil
}

// Compile-time check that Provider implements AnthropicNativeHandler.
var _ provider.AnthropicNativeHandler = (*Provider)(nil)

// HandleAnthropicNative forwards an Anthropic Messages API request to the
// upstream Anthropic API unchanged. On success (2xx), the response is returned
// with the body open. On error (4xx/5xx), the body is consumed and a
// ProviderError is returned to enable the engine's failover logic.
func (p *Provider) HandleAnthropicNative(ctx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	url := p.baseURL + "/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    string(respBody),
		}
	}
	return resp, nil
}

func (p *Provider) Passthrough(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	url := p.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return p.client.Do(req)
}

func (p *Provider) buildRequest(ctx context.Context, body []byte, apiKey string) (*http.Request, error) {
	url := p.baseURL + "/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	return req, nil
}

// extractErrorMessage tries to parse an Anthropic error response, falling back
// to the raw body string.
func extractErrorMessage(body []byte) string {
	var errResp anthropicErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	return string(body)
}

// anthropicStream translates Anthropic SSE events to OpenAI-format SSE chunks.
type anthropicStream struct {
	reader *bufio.Reader
	body   io.ReadCloser
	state  streamState
}

func (s *anthropicStream) Next() ([]byte, error) {
	for {
		eventType, data, err := s.readEvent()
		if err != nil {
			return nil, err
		}

		translated, done, terr := translateStreamEvent(eventType, data, &s.state)
		if terr != nil {
			return nil, terr
		}
		if done {
			return nil, io.EOF
		}
		if translated == nil {
			continue // skip this event
		}

		// Return as "data: {...}" for the transport layer.
		result := make([]byte, 0, 6+len(translated))
		result = append(result, "data: "...)
		result = append(result, translated...)
		return result, nil
	}
}

// readEvent reads the next SSE event (event type + data) from the Anthropic stream.
// Anthropic sends: "event: <type>\ndata: <json>\n\n"
func (s *anthropicStream) readEvent() (eventType, data string, err error) {
	for {
		line, rerr := s.reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return "", "", rerr
		}
		line = strings.TrimSpace(line)

		if line == "" {
			// Empty line = end of event. If we have both fields, return.
			if eventType != "" && data != "" {
				return eventType, data, nil
			}
			if rerr == io.EOF {
				return "", "", io.EOF
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = line[7:]
		} else if strings.HasPrefix(line, "data: ") {
			data = line[6:]
		}

		if rerr == io.EOF {
			if eventType != "" && data != "" {
				return eventType, data, nil
			}
			return "", "", io.EOF
		}
	}
}

func (s *anthropicStream) Close() error {
	return s.body.Close()
}
