package perplexity

import (
	"net/http"

	"github.com/temikus/butter/internal/provider/openaicompat"
)

const defaultBaseURL = "https://api.perplexity.ai"

func New(baseURL string, client *http.Client) *openaicompat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return openaicompat.New("perplexity", baseURL, client)
}
