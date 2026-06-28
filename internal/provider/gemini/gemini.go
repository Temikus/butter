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
		return nil, fmt.Errorf("gemini request failed: %w", &scrubbedError{err})
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
		return nil, fmt.Errorf("gemini stream request failed: %w", &scrubbedError{err})
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
		return nil, &scrubbedError{err}
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// In passthrough mode the client may supply ?key= in the path; scrub it
	// from any transport error so the credential can't leak to callers/logs.
	// This runs only on the failure path — the success path is untouched, so
	// proxy throughput is unaffected.
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &scrubbedError{err}
	}
	return resp, nil
}

// buildRequest constructs the Gemini API request with the model in the URL path.
// The API key is sent via the x-goog-api-key header rather than a query
// parameter so it never lands in the request URL — transport errors (DNS, TLS,
// timeouts) embed the full URL via *url.Error, which the provider wraps and the
// transport layer relays to clients and logs. Keeping the key out of the URL
// prevents the upstream credential from leaking into those error paths.
func (p *Provider) buildRequest(ctx context.Context, model string, stream bool, body []byte, apiKey string) (*http.Request, error) {
	action := "generateContent"
	query := ""
	if stream {
		action = "streamGenerateContent"
		query = "alt=sse"
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:%s", p.baseURL, model, action)
	if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}
	return req, nil
}

// scrubbedError wraps an error so that any Gemini API key in its message is
// redacted, while still exposing the underlying error to errors.Is/errors.As
// via Unwrap. Transport errors are *url.Error values that embed the request URL;
// although buildRequest now keeps the key out of the URL, this is a
// defense-in-depth guard so a key=<secret> query param can never reach
// client-facing error bodies or logs.
type scrubbedError struct{ err error }

func (e *scrubbedError) Error() string { return scrubKeyQuery(e.err.Error()) }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubKeyQuery redacts the value of any "?key=" or "&key=" query parameter,
// leaving the rest of the string untouched. It only matches query-scoped
// occurrences (delimited by ? or &) so unrelated text like "monkey=..." is not
// mangled.
func scrubKeyQuery(s string) string {
	const param = "key="
	var b strings.Builder
	pos := 0
	for pos < len(s) {
		i := strings.Index(s[pos:], param)
		if i < 0 {
			break
		}
		i += pos
		// Only treat as a query parameter when delimited by ? or &.
		if i == 0 || (s[i-1] != '?' && s[i-1] != '&') {
			b.WriteString(s[pos : i+len(param)])
			pos = i + len(param)
			continue
		}
		valStart := i + len(param)
		valEnd := valStart
		for valEnd < len(s) && s[valEnd] != '&' && s[valEnd] != '"' && s[valEnd] != ' ' {
			valEnd++
		}
		b.WriteString(s[pos:valStart])
		if valEnd > valStart {
			b.WriteString("REDACTED")
		}
		pos = valEnd
	}
	b.WriteString(s[pos:])
	return b.String()
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
