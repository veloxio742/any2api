package core

type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type OpenAIChatRequest struct {
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}

type AnthropicMessagesRequest struct {
	Provider string      `json:"provider,omitempty"`
	Model    string      `json:"model"`
	Messages []Message   `json:"messages"`
	System   interface{} `json:"system,omitempty"`
	Stream   bool        `json:"stream,omitempty"`
}

type UnifiedRequest struct {
	ProviderHint    string
	Protocol        string
	Model           string
	Messages        []Message
	System          interface{}
	Stream          bool
	ProviderOptions map[string]string
}

type ProviderCapabilities struct {
	OpenAICompatible    bool `json:"openai_compatible"`
	AnthropicCompatible bool `json:"anthropic_compatible"`
	Tools               bool `json:"tools"`
	Images              bool `json:"images"`
	MultiAccount        bool `json:"multi_account"`
}

type ModelInfo struct {
	Provider      string `json:"provider"`
	PublicModel   string `json:"public_model"`
	UpstreamModel string `json:"upstream_model"`
	OwnedBy       string `json:"owned_by"`
}
