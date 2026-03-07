package orchids

import (
	"context"

	"any2api-go/internal/core"
)

func completeSkeleton(ctx context.Context, reply string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		return reply, nil
	}
}

func streamSkeleton(ctx context.Context, reply string) (<-chan core.TextStreamEvent, error) {
	output := make(chan core.TextStreamEvent, 1)
	go func() {
		defer close(output)
		select {
		case <-ctx.Done():
			output <- core.TextStreamEvent{Err: ctx.Err()}
		case output <- core.TextStreamEvent{Delta: reply}:
		}
	}()
	return output, nil
}
