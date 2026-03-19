package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"testing"
)

// stubProvider is a minimal Provider implementation for testing.
type stubProvider struct {
	name string
}

func (s *stubProvider) Name() string                          { return s.name }
func (s *stubProvider) SupportsOperation(op Operation) bool   { return true }
func (s *stubProvider) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return nil, nil
}
func (s *stubProvider) ChatCompletionStream(ctx context.Context, req *ChatRequest) (Stream, error) {
	return nil, nil
}
func (s *stubProvider) Passthrough(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return nil, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "openai"})
	reg.Register(&stubProvider{name: "anthropic"})

	p, err := reg.Get("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("expected openai, got %s", p.Name())
	}

	p, err = reg.Get("anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic, got %s", p.Name())
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestRegistryList(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "openai"})
	reg.Register(&stubProvider{name: "anthropic"})
	reg.Register(&stubProvider{name: "openrouter"})

	names := reg.List()
	sort.Strings(names)

	expected := []string{"anthropic", "openai", "openrouter"}
	if len(names) != len(expected) {
		t.Fatalf("expected %d providers, got %d", len(expected), len(names))
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, name)
		}
	}
}

func TestRegistryOverwrite(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "openai"})
	reg.Register(&stubProvider{name: "openai"}) // overwrite

	names := reg.List()
	if len(names) != 1 {
		t.Errorf("expected 1 provider after overwrite, got %d", len(names))
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	reg := NewRegistry()
	const goroutines = 50

	// Pre-register a provider so Get has something to find.
	reg.Register(&stubProvider{name: "base"})

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent Register
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			reg.Register(&stubProvider{name: fmt.Sprintf("provider-%d", i)})
		}(i)
	}

	// Concurrent Get
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			reg.Get("base")
		}()
	}

	// Concurrent List
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			reg.List()
		}()
	}

	wg.Wait()

	// Verify all providers were registered.
	names := reg.List()
	// base + goroutines providers
	if len(names) != goroutines+1 {
		t.Errorf("expected %d providers, got %d", goroutines+1, len(names))
	}
}
