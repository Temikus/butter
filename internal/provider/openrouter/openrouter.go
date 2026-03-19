package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/temikus/butter/internal/provider"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Provider implements the provider.Provider interface for OpenRouter.
// OpenRouter exposes an OpenAI-compatible API, so this is largely a passthrough.
type Provider struct {
	baseURL string
	client  *http.Client
	bufPool sync.Pool
}

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

func (p *Provider) Name() string { return "openrouter" }

func (p *Provider) SupportsOperation(op provider.Operation) bool {
	switch op {
	case provider.OpChatCompletion, provider.OpPassthrough, provider.OpModels:
		return true
	}
	return false
}

func (p *Provider) ChatCompletion(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	httpReq, err := p.buildRequest(ctx, "POST", "/chat/completions", req.RawBody, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	return &provider.ChatResponse{
		RawBody:    body,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}, nil
}

func (p *Provider) ChatCompletionStream(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
	httpReq, err := p.buildRequest(ctx, "POST", "/chat/completions", req.RawBody, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter stream request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	return &sseStream{
		reader:  bufio.NewReaderSize(resp.Body, 4096),
		body:    resp.Body,
		bufPool: &p.bufPool,
	}, nil
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

func (p *Provider) buildRequest(ctx context.Context, method, path string, body []byte, apiKey string) (*http.Request, error) {
	url := p.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
}

// sseStream implements provider.Stream for SSE responses.
type sseStream struct {
	reader  *bufio.Reader
	body    io.ReadCloser
	bufPool *sync.Pool
}

func (s *sseStream) Next() ([]byte, error) {
	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				// Process remaining data
			} else {
				return nil, err
			}
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue // Skip empty lines (SSE separator)
		}

		if bytes.HasPrefix(line, []byte("data: ")) {
			data := line[6:] // Strip "data: " prefix
			if bytes.Equal(data, []byte("[DONE]")) {
				return nil, io.EOF
			}
			// Return the raw SSE line including "data: " prefix for relay
			result := make([]byte, len(line))
			copy(result, line)
			return result, nil
		}

		// Pass through non-data SSE lines (event:, id:, retry:)
		if bytes.Contains(line, []byte(":")) {
			result := make([]byte, len(line))
			copy(result, line)
			return result, nil
		}
	}
}

func (s *sseStream) Close() error {
	return s.body.Close()
}

