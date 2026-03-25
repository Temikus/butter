package mistral

import (
	"net/http"

	"github.com/temikus/butter/internal/provider/openaicompat"
)

const defaultBaseURL = "https://api.mistral.ai/v1"

func New(baseURL string, client *http.Client) *openaicompat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return openaicompat.New("mistral", baseURL, client)
}
