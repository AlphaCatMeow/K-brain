package protocol

// GenerateRequest is one model inference, not an agent/tool execution loop.
// Clients select a configured route; credentials and upstream URLs remain local.
type GenerateRequest struct {
	Model           ModelRef         `json:"model"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	Reasoning       string           `json:"reasoning,omitempty"`
	CacheKey        string           `json:"cache_key,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
}

type GenerateResponse struct {
	Version string   `json:"version"`
	Model   ModelRef `json:"model"`
	Message Message  `json:"message"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type GenerateEvent struct {
	Version  string            `json:"version"`
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Response *GenerateResponse `json:"response,omitempty"`
	Error    string            `json:"error,omitempty"`
}
