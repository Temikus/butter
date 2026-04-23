package bedrock

import (
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/temikus/butter/internal/provider"
)

func TestName(t *testing.T) {
	p := New(aws.Config{}, nil)
	if p.Name() != "bedrock" {
		t.Errorf("expected 'bedrock', got %q", p.Name())
	}
}

func TestSupportsOperation(t *testing.T) {
	p := New(aws.Config{}, nil)

	// Bedrock returns false for all standard operations — it is discovered
	// as a failover target via the AnthropicNativeHandler interface, not
	// through the standard dispatch path.
	ops := []provider.Operation{
		provider.OpPassthrough,
		provider.OpChatCompletion,
		provider.OpChatCompletionStream,
		provider.OpEmbeddings,
	}
	for _, op := range ops {
		if p.SupportsOperation(op) {
			t.Errorf("SupportsOperation(%q) = true, want false", op)
		}
	}
}

func TestMapModel_ExplicitMap(t *testing.T) {
	p := New(aws.Config{}, map[string]string{
		"my-custom-model": "custom.my-custom-model-v1:0",
	})

	tests := []struct {
		input string
		want  string
	}{
		// Explicit override takes precedence.
		{"my-custom-model", "custom.my-custom-model-v1:0"},
		// Default map entries still work.
		{"claude-3-5-sonnet-20241022", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"claude-3-opus-20240229", "anthropic.claude-3-opus-20240229-v1:0"},
	}
	for _, tt := range tests {
		if got := p.MapModel(tt.input); got != tt.want {
			t.Errorf("MapModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMapModel_ConventionFallback(t *testing.T) {
	p := New(aws.Config{}, nil)

	// Unknown model falls back to convention: anthropic.{model}-v1:0
	got := p.MapModel("claude-4-future-99990101")
	want := "anthropic.claude-4-future-99990101-v1:0"
	if got != want {
		t.Errorf("MapModel fallback = %q, want %q", got, want)
	}
}

func TestMapModel_DefaultEntries(t *testing.T) {
	p := New(aws.Config{}, nil)

	// Verify key entries from the default map.
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-20250514", "anthropic.claude-opus-4-20250514-v1:0"},
		{"claude-sonnet-4-20250514", "anthropic.claude-sonnet-4-20250514-v1:0"},
		{"claude-3-5-sonnet-20241022", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"claude-3-5-haiku-20241022", "anthropic.claude-3-5-haiku-20241022-v1:0"},
		{"claude-3-haiku-20240307", "anthropic.claude-3-haiku-20240307-v1:0"},
	}
	for _, tt := range tests {
		if got := p.MapModel(tt.input); got != tt.want {
			t.Errorf("MapModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMapModel_OverrideDefaultEntry(t *testing.T) {
	// Override a default entry with a custom mapping.
	p := New(aws.Config{}, map[string]string{
		"claude-3-5-sonnet-20241022": "anthropic.claude-3-5-sonnet-20241022-v99:0",
	})

	got := p.MapModel("claude-3-5-sonnet-20241022")
	want := "anthropic.claude-3-5-sonnet-20241022-v99:0"
	if got != want {
		t.Errorf("MapModel override = %q, want %q", got, want)
	}
}

// mockHTTPError simulates an AWS SDK error with an HTTP status code.
type mockHTTPError struct {
	code int
}

func (e *mockHTTPError) Error() string          { return "mock error" }
func (e *mockHTTPError) HTTPStatusCode() int    { return e.code }

func TestErrorStatusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"aws error with status", &mockHTTPError{code: 429}, 429},
		{"aws error 503", &mockHTTPError{code: 503}, 503},
		{"generic error without status", &provider.ProviderError{Message: "test"}, 502},
		{"plain error", fmt.Errorf("plain"), 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorStatusCode(tt.err); got != tt.want {
				t.Errorf("errorStatusCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Verify that the Provider implements AnthropicNativeHandler at compile time.
var _ provider.AnthropicNativeHandler = (*Provider)(nil)
