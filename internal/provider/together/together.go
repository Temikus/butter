package together

import (
	"net/http"

	"github.com/temikus/butter/internal/provider/openaicompat"
)

const defaultBaseURL = "https://api.together.xyz/v1"

func New(baseURL string, client *http.Client) *openaicompat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return openaicompat.New("together", baseURL, client)
}
