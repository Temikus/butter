// Package sdk provides the shared types used between the Butter host and
// WASM plugins compiled against the Butter plugin ABI.
//
// # ABI Overview
//
// WASM plugins communicate with the Butter host via JSON-encoded messages.
// The host calls exported plugin functions by name, passing a JSON-encoded
// [Request] as input. The plugin returns a JSON-encoded [Response].
//
// All hook exports are optional. If a plugin does not export a given
// function, that hook is silently skipped. Fields with zero values in
// [Response] are treated as "no change" — returning an empty response
// body is equivalent to returning an empty [Response].
//
// # Supported Hooks
//
//   - pre_http  — called before routing; can short-circuit the request
//   - post_http — called after the provider response is received
//   - pre_llm   — called immediately before the provider call; can modify the body
//   - post_llm  — called after the provider call; can modify the response body
//
// # Writing a Plugin
//
// Plugins must be compiled to WebAssembly (WASM) targeting the WASI preview1
// ABI. The recommended toolchain is TinyGo with the Extism Go PDK:
//
//	import pdk "github.com/extism/go-pdk"
//
//	//export pre_http
//	func preHTTP() int32 {
//	    var req sdk.Request
//	    json.Unmarshal(pdk.Input(), &req)
//	    resp := sdk.Response{Metadata: map[string]any{"tagged": true}}
//	    out, _ := json.Marshal(resp)
//	    pdk.Output(out)
//	    return 0
//	}
//
//	func main() {}
//
// See plugins/example-wasm/ in the Butter repository for a complete example.
package sdk

import "encoding/json"

// Request is passed as JSON input to each WASM hook function.
// It encodes the relevant parts of the in-flight Butter [RequestContext].
type Request struct {
	// Hook identifies which hook is being called:
	// "pre_http", "post_http", "pre_llm", or "post_llm".
	Hook string `json:"hook"`

	// Provider is the resolved provider name (e.g. "openai", "anthropic").
	// Empty before routing is resolved (during pre_http).
	Provider string `json:"provider,omitempty"`

	// Model is the model name from the client request.
	Model string `json:"model,omitempty"`

	// Body is the raw JSON request body (pre_http, pre_llm) or
	// response body (post_http, post_llm).
	Body json.RawMessage `json:"body,omitempty"`

	// Headers contains the HTTP request headers (first value per key).
	Headers map[string]string `json:"headers,omitempty"`

	// Metadata is a string-keyed bag that persists across all hooks
	// within a single request. Plugins can read and write arbitrary
	// JSON-compatible values here to pass state between hooks.
	//
	// Well-known keys set by the Butter host:
	//   - "app_key": the resolved application key (btr_-prefixed string),
	//     present when app_keys is enabled and the client sent a valid key.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Response is returned as JSON output from each WASM hook function.
// All fields are optional — zero values mean "no change".
type Response struct {
	// Body replaces the request or response body if non-empty.
	// For pre_http / pre_llm: replaces the outgoing request body.
	// For post_http / post_llm: replaces the response body sent to the client.
	Body json.RawMessage `json:"body,omitempty"`

	// Metadata is merged into the request metadata bag when non-nil.
	// Keys set here are visible to subsequent hooks.
	Metadata map[string]any `json:"metadata,omitempty"`

	// ShortCircuit, when true, stops request processing and returns
	// ShortCircuitBody with ShortCircuitStatus directly to the client,
	// bypassing the provider call entirely.
	// Only honoured from pre_http and pre_llm hooks.
	ShortCircuit bool `json:"short_circuit,omitempty"`

	// ShortCircuitBody is the response body sent to the client when
	// ShortCircuit is true. Should be valid JSON for OpenAI-compatible clients.
	ShortCircuitBody json.RawMessage `json:"short_circuit_body,omitempty"`

	// ShortCircuitStatus is the HTTP status code sent to the client when
	// ShortCircuit is true. Defaults to 400 if zero.
	ShortCircuitStatus int `json:"short_circuit_status,omitempty"`
}
