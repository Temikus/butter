package gemini

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

const defaultBaseURL = "https://generativelanguage.googleapis.com"

// Provider implements provider.Provider for the Google Gemini API.
type Provider struct {
	baseURL string
	client  *http.Client
	bufPool sync.Pool
}

// New creates a Gemini provider. If baseURL is empty, the default is used.
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

func (p *Provider) Name() string { return "gemini" }

func (p *Provider) SupportsOperation(op provider.Operation) bool {
	switch op {
	case provider.OpChatCompletion, provider.OpChatCompletionStream, provider.OpPassthrough:
		return true
	}
	return false
}

func (p *Provider) ChatCompletion(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	body, model, err := translateRequest(req.RawBody)
	if err != nil {
		return nil, fmt.Errorf("translating request: %w", err)
	}

	httpReq, err := p.buildRequest(ctx, model, false, body, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    extractErrorMessage(respBody),
		}
	}

	translated, err := translateResponse(respBody, model)
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
	body, model, err := translateRequest(req.RawBody)
	if err != nil {
		return nil, fmt.Errorf("translating request: %w", err)
	}

	httpReq, err := p.buildRequest(ctx, model, true, body, req.APIKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini stream request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &provider.ProviderError{
			StatusCode: resp.StatusCode,
			Message:    extractErrorMessage(respBody),
		}
	}

	return &geminiStream{
		reader: bufio.NewReaderSize(resp.Body, 4096),
		body:   resp.Body,
		state: streamState{
			model: model,
			id:    fmt.Sprintf("chatcmpl-%s", model),
		},
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

// buildRequest constructs the Gemini API request with model in the URL path
// and API key as a query parameter.
func (p *Provider) buildRequest(ctx context.Context, model string, stream bool, body []byte, apiKey string) (*http.Request, error) {
	action := "generateContent"
	query := ""
	if stream {
		action = "streamGenerateContent"
		query = "alt=sse"
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:%s", p.baseURL, model, action)
	if apiKey != "" {
		if query != "" {
			url += "?" + query + "&key=" + apiKey
		} else {
			url += "?key=" + apiKey
		}
	} else if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func extractErrorMessage(body []byte) string {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	return string(body)
}

// geminiStream translates Gemini SSE events to OpenAI-format SSE chunks.
type geminiStream struct {
	reader *bufio.Reader
	body   io.ReadCloser
	state  streamState
}

func (s *geminiStream) Next() ([]byte, error) {
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
			continue
		}

		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue // skip non-data lines
		}

		data := line[6:]
		translated, done, terr := translateStreamChunk(data, &s.state)
		if terr != nil {
			return nil, terr
		}
		if translated == nil {
			continue
		}

		result := make([]byte, 0, 6+len(translated))
		result = append(result, "data: "...)
		result = append(result, translated...)

		if done {
			// Gemini signals completion via finishReason in the last chunk,
			// not a separate [DONE] marker. We return this final chunk
			// and the transport layer will get io.EOF on the next call.
			return result, nil
		}

		return result, nil
	}
}

func (s *geminiStream) Close() error {
	return s.body.Close()
}
