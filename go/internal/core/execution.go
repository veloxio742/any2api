package core

import "context"

type TextStreamEvent struct {
	Delta string
	Err   error
}

type OpenAIChatProvider interface {
	CompleteOpenAI(ctx context.Context, req UnifiedRequest) (string, error)
}

type OpenAIStreamProvider interface {
	StreamOpenAI(ctx context.Context, req UnifiedRequest) (<-chan TextStreamEvent, error)
}

type AnthropicMessagesProvider interface {
	CompleteAnthropic(ctx context.Context, req UnifiedRequest) (string, error)
}

type AnthropicStreamProvider interface {
	StreamAnthropic(ctx context.Context, req UnifiedRequest) (<-chan TextStreamEvent, error)
}

func CollectTextStream(ctx context.Context, events <-chan TextStreamEvent) (string, error) {
	result := ""
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-events:
			if !ok {
				return result, nil
			}
			if event.Err != nil {
				return "", event.Err
			}
			result += event.Delta
		}
	}
}
