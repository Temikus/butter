package openaicompat

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

// Provider implements the provider.Provider interface for any OpenAI-compatible API.
type Provider struct {
	name    string
	baseURL string
	client  *http.Client
	bufPool sync.Pool
	opts    options
}

// Compile-time check: Provider implements AuthHeaderSetter.
var _ provider.AuthHeaderSetter = (*Provider)(nil)

// options holds optional configuration for the provider.
type options struct {
	authHeaderName string            // custom header name for API key (no "Bearer " prefix)
	queryParams    map[string]string // appended to every request URL
}

// Option configures the openaicompat provider.
type Option func(*options)

// WithAuthHeaderName sets a custom header name for API key authentication.
// When set, the API key is sent as the raw value of this header (no "Bearer " prefix).
func WithAuthHeaderName(name string) Option {
	return func(o *options) { o.authHeaderName = name }
}

// WithQueryParams adds query parameters to every outgoing request URL.
func WithQueryParams(params map[string]string) Option {
	return func(o *options) { o.queryParams = params }
}

func New(name, baseURL string, client *http.Client, opts ...Option) *Provider {
	if client == nil {
		client = &http.Client{}
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return &Provider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		opts:    o,
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, 0, 4096)
				return &buf
			},
		},
	}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) SupportsOperation(op provider.Operation) bool {
	switch op {
	case provider.OpChatCompletion, provider.OpChatCompletionStream, provider.OpPassthrough, provider.OpModels, provider.OpEmbeddings:
		return true
	}
	return false
}

func (p *Provider) Embeddings(ctx context.Context, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	httpReq, err := p.buildRequest(ctx, "POST", "/embeddings", req.RawBody, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s embeddings request failed: %w", p.name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading embeddings response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
		}
	}

	return &provider.EmbeddingResponse{
		RawBody:    body,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}, nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	httpReq, err := p.buildRequest(ctx, "POST", "/chat/completions", req.RawBody, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", p.name, err)
	}
	defer func() { _ = resp.Body.Close() }()

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
		return nil, fmt.Errorf("%s stream request failed: %w", p.name, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
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
	if len(p.opts.queryParams) > 0 {
		q := req.URL.Query()
		for k, v := range p.opts.queryParams {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
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
		if p.opts.authHeaderName != "" {
			req.Header.Set(p.opts.authHeaderName, apiKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	if len(p.opts.queryParams) > 0 {
		q := req.URL.Query()
		for k, v := range p.opts.queryParams {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	return req, nil
}

// SetAuthHeader implements provider.AuthHeaderSetter.
func (p *Provider) SetAuthHeader(headers http.Header, apiKey string) {
	if p.opts.authHeaderName != "" {
		headers.Set(p.opts.authHeaderName, apiKey)
	} else {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
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
