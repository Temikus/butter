package transport

import "bytes"

var streamKey = []byte("stream")

// isStreamRequest reports whether the body sets a top-level "stream": true.
// Walks the outermost object only, skipping nested values, so a `"stream":true`
// substring inside message content can't flip a request into SSE. Allocation-free:
// a full json.Unmarshal here would blow the <50µs proxy overhead budget.
func isStreamRequest(body []byte) bool {
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	i++

	for {
		i = skipJSONSpace(body, i)
		if i >= len(body) {
			return false
		}
		switch body[i] {
		case ',':
			i++
			continue
		case '"':
		default:
			// '}' or malformed input
			return false
		}

		keyStart := i + 1
		keyEnd := scanJSONString(body, i)
		if keyEnd < 0 {
			return false
		}
		key := body[keyStart : keyEnd-1]

		i = skipJSONSpace(body, keyEnd)
		if i >= len(body) || body[i] != ':' {
			return false
		}
		i = skipJSONSpace(body, i+1)

		if bytes.Equal(key, streamKey) {
			return bytes.HasPrefix(body[i:], []byte("true"))
		}

		i = skipJSONValue(body, i)
		if i < 0 {
			return false
		}
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanJSONString returns the index just past the closing quote of the string
// starting at i, or -1 if it is unterminated.
func scanJSONString(b []byte, i int) int {
	i++ // opening quote
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		}
		i++
	}
	return -1
}

// skipJSONValue returns the index just past the value starting at i, or -1 on
// malformed input.
func skipJSONValue(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return scanJSONString(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				i = scanJSONString(b, i)
				if i < 0 {
					return -1
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return -1
	default:
		// number, true, false, null: run to the next delimiter
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i
			}
			i++
		}
		return i
	}
}
