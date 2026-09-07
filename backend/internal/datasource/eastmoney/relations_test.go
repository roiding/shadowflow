package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestDecodeQuoteRowsAcceptsObjectAndPreservesNumericOrder(t *testing.T) {
	rows, err := decodeQuoteRows([]byte(`{"10":{"f12":"third"},"2":{"f12":"second"},"0":{"f12":"first"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || optionalString(rows[0], "f12") != "first" || optionalString(rows[1], "f12") != "second" || optionalString(rows[2], "f12") != "third" {
		t.Fatalf("object diff order was not normalized: %+v", rows)
	}
	rows, err = decodeQuoteRows([]byte(`[{"f12":"array-first"},{"f12":"array-second"}]`))
	if err != nil || len(rows) != 2 || optionalString(rows[1], "f12") != "array-second" {
		t.Fatalf("array diff failed: rows=%+v err=%v", rows, err)
	}
}

func TestFetchBoardCatalogAndConstituents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("fs") == "m:90+t:2+f:!50" {
			_, _ = w.Write([]byte(`{"rc":0,"data":{"total":2,"diff":{"0":{"f12":"BK001","f14":"银行"},"1":{"f12":"BK002","f14":"证券"}}}}`))
			return
		}
		if query.Get("fs") == "b:BK001" {
			_, _ = w.Write([]byte(`{"rc":0,"data":{"total":2,"diff":{"0":{"f12":"000001","f13":0,"f14":"平安银行"},"1":{"f12":"601398","f13":1,"f14":"工商银行"}}}}`))
			return
		}
		t.Fatalf("unexpected quote request: %s", r.URL.String())
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 100).WithQuoteBaseURLs([]string{server.URL})
	boards, err := client.FetchBoardCatalog(context.Background(), graymarket.BoardIndustry)
	if err != nil || len(boards) != 2 || boards[0].Code != "BK001" || boards[1].SourceRank != 2 {
		t.Fatalf("unexpected board catalog: boards=%+v err=%v", boards, err)
	}
	relations, err := client.FetchBoardConstituents(context.Background(), boards[0])
	if err != nil || len(relations) != 2 {
		t.Fatalf("unexpected constituents: relations=%+v err=%v", relations, err)
	}
	first := relations[0]
	if first.StockCode != "000001" || first.StockMarket != 0 || first.BoardCode != "BK001" || first.BoardType != graymarket.BoardIndustry || first.RelationScope != graymarket.RelationScopeBoardConstituents {
		t.Fatalf("unexpected relation mapping: %+v", first)
	}
}

func TestFetchBoardConstituentsDeduplicatesRepeatedRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fs") != "b:BK0457" {
			t.Fatalf("unexpected quote request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"rc":0,"data":{"total":2,"diff":[{"f12":"688226","f13":1,"f14":"重复股"},{"f12":"688226","f13":1,"f14":"重复股"}]}}`))
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 100).WithQuoteBaseURLs([]string{server.URL})
	relations, err := client.FetchBoardConstituents(context.Background(), graymarket.Board{Code: "BK0457", Name: "测试行业", Type: graymarket.BoardIndustry})
	if err != nil {
		t.Fatalf("repeated constituent rows should be tolerated: %v", err)
	}
	if len(relations) != 1 || relations[0].StockCode != "688226" {
		t.Fatalf("unexpected deduplicated relations: %+v", relations)
	}
}

func TestFetchBoardConstituentsStopsAtExactFullPageTotal(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if page := r.URL.Query().Get("pn"); page != "1" {
			t.Fatalf("requested page %s after reaching the declared total", page)
		}
		_, _ = w.Write([]byte(`{"rc":0,"data":{"total":2,"diff":[{"f12":"000001","f13":0,"f14":"股票一"},{"f12":"000002","f13":0,"f14":"股票二"}]}}`))
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 2).WithQuoteBaseURLs([]string{server.URL})
	relations, err := client.FetchBoardConstituents(context.Background(), graymarket.Board{Code: "BK1447", Name: "测试行业", Type: graymarket.BoardIndustry})
	if err != nil {
		t.Fatalf("exact full page should finish without requesting another page: %v", err)
	}
	if len(relations) != 2 || calls != 1 {
		t.Fatalf("unexpected result: relations=%+v calls=%d", relations, calls)
	}
}

func TestFetchBoardCatalogRejectsUnderreportedTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"rc":0,"data":{"total":1,"diff":[{"f12":"BK001","f14":"银行"},{"f12":"BK002","f14":"证券"}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"rc":0,"data":{"total":1,"diff":[{"f12":"BK003","f14":"保险"}]}}`))
		default:
			t.Fatalf("unexpected page: %s", r.URL.Query().Get("pn"))
		}
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 2).WithQuoteBaseURLs([]string{server.URL})
	if _, err := client.FetchBoardCatalog(context.Background(), graymarket.BoardIndustry); err == nil {
		t.Fatal("under-reported catalog total was accepted")
	}
}

func TestFetchBoardCatalogRejectsInvalidTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rc":0,"data":{"total":0,"diff":[{"f12":"BK001","f14":"银行"}]}}`))
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 100).WithQuoteBaseURLs([]string{server.URL})
	if _, err := client.FetchBoardCatalog(context.Background(), graymarket.BoardIndustry); err == nil {
		t.Fatal("catalog with invalid total was accepted")
	}
}

func TestQuoteRequestFallsBackAfterEmptyPrimaryResponse(t *testing.T) {
	primaryCalls, fallbackCalls := 0, 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		_, _ = w.Write([]byte(`{"rc":0,"data":{"total":1,"diff":{"0":{"f12":"BK001","f14":"银行"}}}}`))
	}))
	defer fallback.Close()
	client := NewClient("unused", fallback.Client(), 100).WithQuoteBaseURLs([]string{primary.URL, fallback.URL})
	boards, err := client.FetchBoardCatalog(context.Background(), graymarket.BoardIndustry)
	if err != nil || len(boards) != 1 || primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("fallback failed: boards=%+v primary=%d fallback=%d err=%v", boards, primaryCalls, fallbackCalls, err)
	}
}

func TestFetchBoardQuotesMapsIndustryAndConceptFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("fs") != "m:90+t:2+f:!50" && query.Get("fs") != "m:90+t:3+f:!50" {
			t.Fatalf("unexpected board quote filter: %s", query.Get("fs"))
		}
		if query.Get("fields") == "" || query.Get("fields") == "f12,f14,f3" {
			t.Fatalf("board quote fields were not requested: %s", query.Get("fields"))
		}
		_, _ = w.Write([]byte(`{"rc":0,"data":{"total":1,"diff":{"0":{"f2":10.5,"f3":2.5,"f4":0.25,"f5":1234,"f6":5678900,"f7":3.5,"f8":2.25,"f12":"BK001","f13":90,"f14":"测试板块","f15":10.8,"f16":9.9,"f17":10.0,"f18":10.2,"f124":1786693171}}}}`))
	}))
	defer server.Close()
	client := NewClient("unused", server.Client(), 100).WithQuoteBaseURLs([]string{server.URL})
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		quotes, err := client.FetchBoardQuotes(context.Background(), rankType)
		if err != nil || len(quotes) != 1 {
			t.Fatalf("unexpected %s board quotes: %+v err=%v", rankType, quotes, err)
		}
		quote := quotes[0]
		if quote.BoardCode != "BK001" || quote.BoardMarket != 90 || quote.Turnover != 5678900 || quote.TurnoverRate != 0.0225 || !quote.Available {
			t.Fatalf("unexpected mapped %s board quote: %+v", rankType, quote)
		}
	}
}

func TestFetchBoardListsStayCompleteWhenGainsChangeAndBoardsAreAdded(t *testing.T) {
	for _, list := range []string{"catalog", "quotes"} {
		for _, boardType := range []graymarket.BoardType{graymarket.BoardIndustry, graymarket.BoardConcept} {
			t.Run(list+"/"+string(boardType), func(t *testing.T) {
				var calls, total atomic.Int32
				handler := boardListTestTransport(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					count := int(total.Load())
					query := r.URL.Query()
					page, err := strconv.Atoi(query.Get("pn"))
					if err != nil || page < 1 || page > (count+1)/2 || query.Get("pz") != "2" || query.Get("po") != "1" {
						t.Errorf("unexpected pagination request: %s", r.URL)
						http.Error(w, "invalid page", http.StatusBadRequest)
						return
					}
					typeCode := "2"
					if boardType == graymarket.BoardConcept {
						typeCode = "3"
					}
					if query.Get("fs") != "m:90+t:"+typeCode+"+f:!50" {
						t.Errorf("unexpected board filter: %s", query.Get("fs"))
					}
					type row struct {
						Code   string `json:"f12"`
						Change int    `json:"f3"`
					}
					rows := make([]row, count)
					for index := range rows {
						rows[index] = row{Code: fmt.Sprintf("BK%03d", index+1), Change: count - index}
					}
					// The third board overtakes the second after page one. A gain
					// sort repeats BK002 on page two and never returns BK003.
					if page > 1 {
						rows[1].Change, rows[2].Change = rows[2].Change, rows[1].Change
					}
					switch query.Get("fid") {
					case "f12":
						sort.Slice(rows, func(i, j int) bool { return rows[i].Code > rows[j].Code })
					case "f3":
						sort.Slice(rows, func(i, j int) bool { return rows[i].Change > rows[j].Change })
					default:
						t.Errorf("unsupported sort field: %s", query.Get("fid"))
						http.Error(w, "invalid sort", http.StatusBadRequest)
						return
					}
					start := (page - 1) * 2
					if err := json.NewEncoder(w).Encode(map[string]any{
						"rc": 0, "data": map[string]any{"total": count, "diff": rows[start:min(start+2, count)]},
					}); err != nil {
						t.Error(err)
					}
				})
				client := NewClient("unused", &http.Client{Transport: handler}, 2).WithQuoteBaseURLs([]string{"https://quote.test"})
				for _, count := range []int{4, 5} {
					total.Store(int32(count))
					calls.Store(0)
					codes, err := fetchTestBoardListCodes(client, list, boardType)
					if err != nil {
						t.Fatalf("moving gains broke board pagination with %d boards: %v", count, err)
					}
					if list == "catalog" && !sort.StringsAreSorted(codes) {
						t.Fatalf("catalog order changed: %v", codes)
					}
					sort.Strings(codes)
					want := make([]string, count)
					for index := range want {
						want[index] = fmt.Sprintf("BK%03d", index+1)
					}
					if !slices.Equal(codes, want) || int(calls.Load()) != (count+1)/2 {
						t.Fatalf("incomplete board list: got=%v want=%v calls=%d", codes, want, calls.Load())
					}
				}
			})
		}
	}
}

func TestFetchBoardListsRejectInconsistentPages(t *testing.T) {
	for _, list := range []string{"catalog", "quotes"} {
		for _, tc := range []struct {
			name       string
			secondPage string
			wantError  string
		}{
			{"duplicate", `{"rc":0,"data":{"total":4,"diff":[{"f12":"BK002"},{"f12":"BK003"}]}}`, "duplicate"},
			{"truncated", `{"rc":0,"data":{"total":4,"diff":[{"f12":"BK003"}]}}`, "incomplete"},
			{"total_increased", `{"rc":0,"data":{"total":5,"diff":[{"f12":"BK003"},{"f12":"BK004"}]}}`, "total changed"},
			{"total_decreased", `{"rc":0,"data":{"total":3,"diff":[{"f12":"BK003"},{"f12":"BK004"}]}}`, "total changed"},
		} {
			t.Run(list+"/"+tc.name, func(t *testing.T) {
				handler := boardListTestTransport(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Query().Get("pn") {
					case "1":
						_, _ = w.Write([]byte(`{"rc":0,"data":{"total":4,"diff":[{"f12":"BK001"},{"f12":"BK002"}]}}`))
					case "2":
						_, _ = w.Write([]byte(tc.secondPage))
					default:
						t.Errorf("unexpected page: %s", r.URL.Query().Get("pn"))
						http.Error(w, "invalid page", http.StatusBadRequest)
					}
				})
				client := NewClient("unused", &http.Client{Transport: handler}, 2).WithQuoteBaseURLs([]string{"https://quote.test"})
				codes, err := fetchTestBoardListCodes(client, list, graymarket.BoardIndustry)
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || len(codes) != 0 {
					t.Fatalf("invalid board list was not rejected: codes=%v err=%v", codes, err)
				}
			})
		}
	}
}

type boardListTestTransport http.HandlerFunc

func (handler boardListTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder.Result(), nil
}

func fetchTestBoardListCodes(client *Client, list string, boardType graymarket.BoardType) ([]string, error) {
	var codes []string
	if list == "catalog" {
		boards, err := client.FetchBoardCatalog(context.Background(), boardType)
		if err != nil {
			return nil, err
		}
		for index, board := range boards {
			if board.Type != boardType || board.SourceRank != index+1 {
				return nil, fmt.Errorf("invalid catalog mapping: %+v", board)
			}
			codes = append(codes, board.Code)
		}
	} else {
		quotes, err := client.FetchBoardQuotes(context.Background(), graymarket.RankType(boardType))
		if err != nil {
			return nil, err
		}
		for _, quote := range quotes {
			codes = append(codes, quote.BoardCode)
		}
	}
	return codes, nil
}
