package collector

import (
	"context"
	"fmt"
)

func (service *Service) PrepareUpstream(ctx context.Context, purpose string) error {
	concurrency := 1
	requests := -1
	if purpose == "stock-kline" || purpose == "end-of-day" {
		concurrency = 4
	}
	return service.PrepareUpstreamRequests(ctx, purpose, requests, concurrency)
}

func (service *Service) PrepareUpstreamRequests(ctx context.Context, purpose string, requests int, concurrency int) error {
	if requests == 0 {
		return ctx.Err()
	}
	if preparer, ok := service.source.(interface {
		PrepareUpstreamRequests(context.Context, string, int, int) error
	}); ok {
		if err := preparer.PrepareUpstreamRequests(ctx, purpose, requests, concurrency); err != nil {
			return fmt.Errorf("prepare %s upstream: %w", purpose, err)
		}
		return ctx.Err()
	}
	if preparer, ok := service.source.(interface {
		PrepareUpstream(context.Context, string) error
	}); ok {
		if err := preparer.PrepareUpstream(ctx, purpose); err != nil {
			return fmt.Errorf("prepare %s upstream: %w", purpose, err)
		}
		return ctx.Err()
	}
	return nil
}
