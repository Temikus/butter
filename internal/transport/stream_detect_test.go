package transport

import "testing"

func TestIsStreamRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "top-level stream true",
			body: `{"model":"gpt-4","stream":true}`,
			want: true,
		},
		{
			name: "top-level stream false",
			body: `{"model":"gpt-4","stream":false}`,
			want: false,
		},
		{
			name: "stream absent",
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "stream true inside message content",
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"what does \"stream\":true do?"}]}`,
			want: false,
		},
		{
			name: "stream false at top level, true inside content",
			body: `{"messages":[{"role":"user","content":"send \"stream\": true please"}],"stream":false}`,
			want: false,
		},
		{
			name: "stream true after content mentioning it",
			body: `{"messages":[{"role":"user","content":"\"stream\":false"}],"stream":true}`,
			want: true,
		},
		{
			name: "nested stream key only",
			body: `{"model":"gpt-4","options":{"stream":true}}`,
			want: false,
		},
		{
			name: "whitespace around colon and value",
			body: `{ "model" : "gpt-4" , "stream" :   true }`,
			want: true,
		},
		{
			name: "newlines and tabs",
			body: "{\n\t\"model\": \"gpt-4\",\n\t\"stream\": true\n}",
			want: true,
		},
		{
			name: "leading whitespace before object",
			body: "  \n{\"stream\":true}",
			want: true,
		},
		{
			name: "stream first key",
			body: `{"stream":true,"model":"gpt-4"}`,
			want: true,
		},
		{
			name: "stream after nested array of objects",
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}],"tools":[],"stream":true}`,
			want: true,
		},
		{
			name: "escaped quote before stream key",
			body: `{"model":"a\"b","stream":true}`,
			want: true,
		},
		{
			name: "stream null",
			body: `{"stream":null}`,
			want: false,
		},
		{
			name: "numbers and booleans before stream",
			body: `{"temperature":0,"max_tokens":1024,"logprobs":false,"stream":true}`,
			want: true,
		},
		{
			name: "duplicate stream keys, last wins true",
			body: `{"stream":false,"model":"gpt-4","stream":true}`,
			want: true,
		},
		{
			name: "duplicate stream keys, last wins false",
			body: `{"stream":true,"model":"gpt-4","stream":false}`,
			want: false,
		},
		{
			name: "duplicate stream keys, nested object between",
			body: `{"stream":true,"options":{"stream":true},"stream":false}`,
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
		{
			name: "not an object",
			body: `["stream",true]`,
			want: false,
		},
		{
			name: "truncated body",
			body: `{"messages":[{"role":"user","content":"hi"`,
			want: false,
		},
		{
			name: "empty object",
			body: `{}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStreamRequest([]byte(tt.body)); got != tt.want {
				t.Errorf("isStreamRequest(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func BenchmarkIsStreamRequest(b *testing.B) {
	bodies := [][]byte{
		[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream": false}`),
		[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`),
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = isStreamRequest(bodies[i%len(bodies)])
	}
}
