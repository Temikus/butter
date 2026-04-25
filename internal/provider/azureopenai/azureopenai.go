package azureopenai

import (
	"net/http"

	"github.com/temikus/butter/internal/provider/openaicompat"
)

// New creates an Azure OpenAI provider.
// name is the provider identifier (e.g. "azureopenai" or "azureopenai-gpt4o").
// baseURL should include the deployment path, e.g.:
//
//	https://myresource.openai.azure.com/openai/deployments/my-gpt4o
//
// apiVersion is the Azure API version string (e.g. "2024-10-21").
func New(name, baseURL, apiVersion string, client *http.Client) *openaicompat.Provider {
	return openaicompat.New(name, baseURL, client,
		openaicompat.WithAuthHeaderName("api-key"),
		openaicompat.WithQueryParams(map[string]string{
			"api-version": apiVersion,
		}),
	)
}
