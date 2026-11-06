package eastmoney

import (
	"context"
	"fmt"
)

func (client *Client) PrepareUpstream(ctx context.Context, purpose string) error {
	concurrency := 1
	requests := -1
	if purpose == "stock-kline" || purpose == "end-of-day" {
		concurrency = 4
	}
	return client.PrepareUpstreamRequests(ctx, purpose, requests, concurrency)
}

func (client *Client) PrepareUpstreamRequests(ctx context.Context, purpose string, requests int, concurrency int) error {
	if client == nil || client.guard == nil {
		return nil
	}
	if concurrency <= 0 {
		concurrency = 1
		if purpose == "stock-kline" || purpose == "end-of-day" {
			concurrency = 4
		}
	}
	var targets []string
	switch purpose {
	case "boards":
		targets = []string{client.baseURL}
	case "relations":
		targets = client.quoteBaseURLs
	case "daily-close":
		targets = append([]string{client.baseURL}, client.quoteBaseURLs...)
	case "end-of-day":
		targets = append([]string{client.baseURL, client.darkTradeTickBaseURL}, client.quoteBaseURLs...)
	case "stock-kline":
		targets = []string{client.stockKlineBaseURL}
	default:
		return fmt.Errorf("unknown upstream preparation purpose %q", purpose)
	}
	return client.guard.PrepareForRequests(ctx, requests, concurrency, targets...)
}
