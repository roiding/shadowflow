package eastmoney

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestFetchAllMapsBoardAndPaginates(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		if r.URL.Query().Get("StartPage") == "1" {
			_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK0448","5":103105,"6":100,"7":200,"8":300,"9":10,"10":2,"11":0.1,"12":0.8,"13":12345,"14":0.02,"15":"leader","16":"industry","20":"000001","21":1}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK0002","5":103105,"6":90,"7":190,"8":280,"16":"second","21":2}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client(), 1)
	snapshot, err := client.FetchAll(context.Background(), graymarket.RankIndustry, "20260812", time.Date(2026, 8, 12, 10, 31, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(snapshot.Records) != 2 || snapshot.TradeDate != "2026-08-12" {
		t.Fatalf("unexpected snapshot: requests=%d date=%s records=%d", requests, snapshot.TradeDate, len(snapshot.Records))
	}
	first := snapshot.Records[0]
	if first.Code != "BK0448" || first.Name != "industry" || first.DarkMoney != 100 || first.QuoteTime != "103105" {
		t.Fatalf("unexpected first row: %+v", first)
	}
	if first.Rank != 1 || snapshot.Records[1].Rank != 2 {
		t.Fatalf("unexpected response-order ranks: first=%d second=%d", first.Rank, snapshot.Records[1].Rank)
	}
}

func TestFetchAllRejectsIncompleteSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK1","6":100,"16":"one","21":1}]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client(), 100)
	_, err := client.FetchAll(context.Background(), graymarket.RankIndustry, "20260812", time.Now())
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete snapshot error, got %v", err)
	}
}

func TestFetchAllRejectsDuplicateCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("StartPage") == "1" {
			_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK1","6":100,"16":"one","21":1}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK1","6":90,"16":"duplicate","21":2}]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client(), 1)
	_, err := client.FetchAll(context.Background(), graymarket.RankIndustry, "20260812", time.Now())
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestFetchAllPreservesResponseOrderWithoutAssumingUpstreamSort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260812,"2":2,"data":[{"3":90,"4":"BK1","6":100,"16":"one","21":2},{"3":90,"4":"BK2","6":110,"16":"two","21":1}]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client(), 100)
	snapshot, err := client.FetchAll(context.Background(), graymarket.RankIndustry, "20260812", time.Now())
	if err != nil {
		t.Fatalf("expected unordered response to be accepted, got %v", err)
	}
	if len(snapshot.Records) != 2 || snapshot.Records[0].Code != "BK1" || snapshot.Records[1].Code != "BK2" {
		t.Fatalf("response order was not preserved: %+v", snapshot.Records)
	}
	if snapshot.Records[0].Rank != 1 || snapshot.Records[1].Rank != 2 {
		t.Fatalf("unexpected response-order ranks: %+v", snapshot.Records)
	}
}

func TestFetchAllClassifiesDecodeErrors(t *testing.T) {
	tests := map[string][]byte{
		"invalid json":  []byte(`{"errid":`),
		"invalid bytes": []byte{0xff, 0xfe, 0xfd},
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=UTF-8")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			client := NewClient(server.URL, server.Client(), 100)
			_, err := client.FetchAll(context.Background(), graymarket.RankConcept, "20260812", time.Now())
			if !errors.Is(err, graymarket.ErrDecode) {
				t.Fatalf("expected ErrDecode, got %v", err)
			}
		})
	}
}

func TestFetchAllReturnsNoData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errid":-2,"errmsg":"no data"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client(), 100)
	_, err := client.FetchAll(context.Background(), graymarket.RankConcept, "20260813", time.Now())
	if err != graymarket.ErrNoData {
		t.Fatalf("expected ErrNoData, got %v", err)
	}
}
