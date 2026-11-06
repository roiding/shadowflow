package eastmoney

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

type requestsTransport struct {
	requestCount int
	concurrency  int
	urls         []string
	called       bool
}

func (transport *requestsTransport) PrepareForRequests(_ context.Context, requestCount, concurrency int, rawURLs ...string) error {
	transport.called = true
	transport.requestCount = requestCount
	transport.concurrency = concurrency
	transport.urls = append([]string(nil), rawURLs...)
	return nil
}

func (*requestsTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("preparation must not fetch business data")
}

type legacyPreparationTransport struct {
	urls []string
}

func (transport *legacyPreparationTransport) Prepare(_ context.Context, rawURLs ...string) error {
	transport.urls = append([]string(nil), rawURLs...)
	return nil
}

func (*legacyPreparationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("preparation must not fetch business data")
}

func TestPreparationScopesMatchCollectorEndpoints(testingT *testing.T) {
	for _, fixture := range []struct {
		purpose             string
		expectedRequests    int
		expectedConcurrency int
		expectedURLs        []string
	}{
		{"relations", -1, 1, []string{"https://quotes.example", "https://delay.example"}},
		{"stock-kline", -1, 4, []string{"https://history.example/api/qt/stock/kline/get"}},
		{"boards", -1, 1, []string{"https://money.example/darktrade"}},
		{"daily-close", -1, 1, []string{"https://money.example/darktrade", "https://quotes.example", "https://delay.example"}},
		{"end-of-day", -1, 4, []string{"https://money.example/darktrade", "https://money.example/darktradetick", "https://quotes.example", "https://delay.example"}},
	} {
		testingT.Run(fixture.purpose, func(test *testing.T) {
			transport := &requestsTransport{}
			client := NewClient("https://money.example/darktrade", &http.Client{Transport: transport}, 100).
				WithQuoteBaseURLs([]string{"https://quotes.example", "https://delay.example"}).
				WithStockKlineBaseURL("https://history.example/api/qt/stock/kline/get").
				WithDarkTradeTickBaseURL("https://money.example/darktradetick")
			if err := client.PrepareUpstream(context.Background(), fixture.purpose); err != nil {
				test.Fatal(err)
			}
			if !transport.called {
				test.Fatal("expected PrepareForRequests to be called")
			}
			if transport.requestCount != fixture.expectedRequests {
				test.Fatalf("wrong requests: got %d want %d", transport.requestCount, fixture.expectedRequests)
			}
			if transport.concurrency != fixture.expectedConcurrency {
				test.Fatalf("wrong concurrency: got %d want %d", transport.concurrency, fixture.expectedConcurrency)
			}
			if !reflect.DeepEqual(transport.urls, fixture.expectedURLs) {
				test.Fatalf("wrong prewarm targets: %v", transport.urls)
			}
		})
	}
	testingT.Run("custom-requests-and-concurrency", func(test *testing.T) {
		transport := &requestsTransport{}
		client := NewClient("https://money.example/darktrade", &http.Client{Transport: transport}, 100).
			WithStockKlineBaseURL("https://history.example/api/qt/stock/kline/get")
		if err := client.PrepareUpstreamRequests(context.Background(), "stock-kline", 12, 3); err != nil {
			test.Fatal(err)
		}
		if !transport.called {
			test.Fatal("expected PrepareForRequests to be called")
		}
		if transport.requestCount != 12 || transport.concurrency != 3 {
			test.Fatalf("unexpected call args: requests=%d, concurrency=%d", transport.requestCount, transport.concurrency)
		}
		expectedURLs := []string{"https://history.example/api/qt/stock/kline/get"}
		if !reflect.DeepEqual(transport.urls, expectedURLs) {
			test.Fatalf("wrong prewarm targets: %v", transport.urls)
		}
	})
	testingT.Run("unknown-purpose", func(test *testing.T) {
		transport := &requestsTransport{}
		client := NewClient("https://money.example/darktrade", &http.Client{Transport: transport}, 100)
		if err := client.PrepareUpstreamRequests(context.Background(), "invalid-purpose", 10, 4); err == nil {
			test.Fatal("expected error for unknown purpose")
		}
	})
	testingT.Run("zero-requests", func(test *testing.T) {
		transport := &requestsTransport{}
		client := NewClient("https://money.example/darktrade", &http.Client{Transport: transport}, 100).
			WithStockKlineBaseURL("https://history.example/api/qt/stock/kline/get")
		if err := client.PrepareUpstreamRequests(context.Background(), "stock-kline", 0, 4); err != nil {
			test.Fatal(err)
		}
		if transport.called {
			test.Fatalf("zero requests should bypass transport preparation, but transport was called: %v", transport.urls)
		}
	})
}

func TestPreparationFallbackToLegacyPrepare(testingT *testing.T) {
	transport := &legacyPreparationTransport{}
	client := NewClient("https://money.example/darktrade", &http.Client{Transport: transport}, 100).
		WithStockKlineBaseURL("https://history.example/api/qt/stock/kline/get")
	if err := client.PrepareUpstreamRequests(context.Background(), "stock-kline", 5, 2); err != nil {
		testingT.Fatal(err)
	}
	expectedURLs := []string{"https://history.example/api/qt/stock/kline/get"}
	if !reflect.DeepEqual(transport.urls, expectedURLs) {
		testingT.Fatalf("legacy fallback target mismatch: %v", transport.urls)
	}
}
