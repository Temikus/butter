package groq

import (
	"net/http"

	"github.com/temikus/butter/internal/provider/openaicompat"
)

const defaultBaseURL = "https://api.groq.com/openai/v1"

func New(baseURL string, client *http.Client) *openaicompat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return openaicompat.New("groq", baseURL, client)
}
