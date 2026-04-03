package provider

// ModelInfo represents a single model entry in the OpenAI-compatible models list.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`   // always "model"
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelListResponse is the OpenAI-compatible response for GET /v1/models.
type ModelListResponse struct {
	Object string      `json:"object"` // always "list"
	Data   []ModelInfo `json:"data"`
}
