package appkey

import "testing"

func TestExtractUsage(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	pt, ct := ExtractUsage(body)
	if pt != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", pt)
	}
	if ct != 5 {
		t.Errorf("expected completion_tokens=5, got %d", ct)
	}
}

func TestExtractUsage_NoUsageField(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","choices":[]}`)
	pt, ct := ExtractUsage(body)
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros, got %d, %d", pt, ct)
	}
}

func TestExtractUsage_InvalidJSON(t *testing.T) {
	pt, ct := ExtractUsage([]byte(`not json`))
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros for invalid JSON, got %d, %d", pt, ct)
	}
}

func TestExtractUsage_Empty(t *testing.T) {
	pt, ct := ExtractUsage(nil)
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros for nil body, got %d, %d", pt, ct)
	}
}

func TestExtractAnthropicUsage(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","usage":{"input_tokens":42,"output_tokens":17}}`)
	in, out := ExtractAnthropicUsage(body)
	if in != 42 {
		t.Errorf("expected input_tokens=42, got %d", in)
	}
	if out != 17 {
		t.Errorf("expected output_tokens=17, got %d", out)
	}
}

func TestExtractAnthropicUsage_NoUsageField(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","content":[]}`)
	in, out := ExtractAnthropicUsage(body)
	if in != 0 || out != 0 {
		t.Errorf("expected zeros, got %d, %d", in, out)
	}
}

func TestExtractAnthropicUsage_InvalidJSON(t *testing.T) {
	in, out := ExtractAnthropicUsage([]byte(`not json`))
	if in != 0 || out != 0 {
		t.Errorf("expected zeros for invalid JSON, got %d, %d", in, out)
	}
}

func TestExtractAnthropicUsage_Empty(t *testing.T) {
	in, out := ExtractAnthropicUsage(nil)
	if in != 0 || out != 0 {
		t.Errorf("expected zeros for nil body, got %d, %d", in, out)
	}
}
