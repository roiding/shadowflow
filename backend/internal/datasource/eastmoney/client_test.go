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

func TestFetchAllMapsStockCodeSeparatelyFromQuoteTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("market") != "" || query.Get("datetype") != "" {
			t.Fatalf("stock request must not select a board market: %s", r.URL.RawQuery)
		}
		if query.Get("version") != "101" || query.Get("sortflag") != "6" || query.Get("desc") != "1" {
			t.Fatalf("unexpected stock request parameters: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"errid":0,"errmsg":"success","1":20260813,"2":1,"data":[{"3":0,"4":"000938","5":151000,"6":100,"16":"紫光股份","21":1}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client(), 100)
	snapshot, err := client.FetchAll(context.Background(), graymarket.RankStock, "20260813", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 1 {
		t.Fatalf("expected one stock record, got %d", len(snapshot.Records))
	}
	record := snapshot.Records[0]
	if record.Code != "000938" || record.QuoteTime != "151000" || record.Name != "紫光股份" {
		t.Fatalf("stock code and quote time were not mapped independently: %+v", record)
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

func TestFetchStockQuotesMapsLatestRowsAndPreservesMissingConstituents(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/qt/ulist.np/get" {
			t.Fatalf("unexpected quote path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Query().Get("secids"), "1.600001") || !strings.Contains(r.URL.Query().Get("secids"), "0.000002") {
			t.Fatalf("constituent markets/codes were not encoded: %s", r.URL.Query().Get("secids"))
		}
		_, _ = w.Write([]byte(`{"rc":0,"message":"","data":{"total":2,"diff":[{"f2":12.34,"f3":1.25,"f4":0.15,"f5":1234,"f6":5678900,"f7":3.5,"f8":2.25,"f12":"600001","f13":1,"f14":"测试股份","f15":12.8,"f16":11.9,"f17":12.05,"f18":12.19,"f124":"2026-08-14 10:31:00"},{"f2":"-","f3":"-","f4":"-","f5":"-","f6":"-","f12":"000003","f13":0,"f14":"停牌股份","f18":8.88}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client(), 100).WithQuoteBaseURLs([]string{server.URL})
	relations := []graymarket.StockBoardRelation{
		{StockCode: "600001", StockMarket: 1, StockName: "测试股份"},
		{StockCode: "000002", StockMarket: 0, StockName: "未返回股份"},
		{StockCode: "000003", StockMarket: 0, StockName: "停牌股份"},
	}
	quotes, err := client.FetchStockQuotes(context.Background(), relations)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(quotes) != 3 {
		t.Fatalf("unexpected quote result: requests=%d quotes=%d", requests, len(quotes))
	}
	if !quotes[0].Available || quotes[0].LatestPrice != 12.34 || quotes[0].ChangePct != 0.0125 || quotes[0].Turnover != 5678900 || quotes[0].QuoteTime != "2026-08-14 10:31:00" {
		t.Fatalf("unexpected mapped quote: %+v", quotes[0])
	}
	if quotes[0].OpenPrice != 12.05 || quotes[0].HighPrice != 12.8 || quotes[0].LowPrice != 11.9 || quotes[0].PreviousClose != 12.19 || quotes[0].TurnoverRate != 0.0225 || quotes[0].Amplitude != 0.035 {
		t.Fatalf("daily OHLC quote fields were not mapped: %+v", quotes[0])
	}
	if quotes[1].Available || quotes[1].StockCode != "000002" || quotes[1].StockName != "未返回股份" {
		t.Fatalf("missing constituent was not preserved: %+v", quotes[1])
	}
	if quotes[2].Available || quotes[2].LatestPrice != 0 || quotes[2].PreviousClose != 8.88 {
		t.Fatalf("suspended quote should be unavailable: %+v", quotes[2])
	}
}
