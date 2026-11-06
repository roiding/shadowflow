package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestFailedRequestRefillsSmallBatchThenRetriesSameKline(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 4)
	fixture.emptyOnAcquire = 2
	target := "https://push2his.eastmoney.com/api/qt/stock/kline/get?secid=1.600000&beg=20261105&end=20261105&klt=5&fqt=0"
	expected, err := url.Parse(target)
	if err != nil {
		testingT.Fatal(err)
	}
	businessCalls := 0
	transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
		businessCalls++
		if request.URL.Path != expected.Path || request.URL.RawQuery != expected.RawQuery {
			testingT.Errorf("refill changed original K-line request: %s", request.URL)
		}
		if businessCalls == 1 {
			return nil, errors.New("proxy connection interrupted")
		}
		return managedResponse(http.StatusOK, `{"rc":0,"data":{}}`), nil
	}), manager, time.Second)
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 4})
	if err := guard.PrepareForRequests(context.Background(), 5000, 4, target); err != nil {
		testingT.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		testingT.Fatal(err)
	}
	response, err := guard.Do(context.Background(), request)
	if err != nil {
		testingT.Fatal(err)
	}
	_ = response.Body.Close()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if businessCalls != 2 || fixture.prewarms != 2 || fixture.acquires != 3 || fixture.feedback != 1 ||
		fixture.counts["warm-1"] != 6 || fixture.counts["warm-2"] != 6 {
		testingT.Fatalf("unexpected refill lifecycle: calls=%d prewarm=%d acquire=%d feedback=%d counts=%v",
			businessCalls, fixture.prewarms, fixture.acquires, fixture.feedback, fixture.counts)
	}
}
