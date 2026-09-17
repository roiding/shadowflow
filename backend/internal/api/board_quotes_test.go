package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/quote"
	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

type boardQuotePayload struct {
	Data []boardStockQuote `json:"data"`
	Meta struct {
		QuoteStatus       string `json:"quote_status"`
		QuoteRefreshing   bool   `json:"quote_refreshing"`
		QuoteAvailable    bool   `json:"quote_available"`
		QuotedCount       int    `json:"quoted_count"`
		QuoteError        string `json:"quote_error"`
		DarkDataAvailable bool   `json:"dark_data_available"`
		DarkDataCount     int    `json:"dark_data_count"`
	} `json:"meta"`
}

func seedQuoteRelations(t *testing.T, store *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	run := repository.RelationSyncRun{RunID: "quote-relations", TradeDate: "2026-09-14", Status: repository.RunRunning, StartedAt: at}
	if err := store.StartRelationSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	var relations []graymarket.StockBoardRelation
	for _, boardType := range []graymarket.BoardType{graymarket.BoardIndustry, graymarket.BoardConcept} {
		boardCode := "BK001"
		if boardType == graymarket.BoardConcept {
			boardCode = "BK101"
		}
		for index, code := range []string{"000001", "000002"} {
			relations = append(relations, graymarket.StockBoardRelation{StockCode: code, StockName: code,
				BoardCode: boardCode, BoardName: string(boardType), BoardType: boardType, SourceOrder: index + 1,
				RelationSource: graymarket.RelationSourceQuoteClist, RelationScope: graymarket.RelationScopeBoardConstituents, DetectedAt: at, RawData: `{}`})
		}
	}
	if err := store.StageRelations(ctx, run.RunID, relations); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRelationScan(ctx, run.RunID, run.TradeDate, at); err != nil {
		t.Fatal(err)
	}
}

func getBoardQuotes(t *testing.T, server *Server, boardType string) boardQuotePayload {
	t.Helper()
	boardCode := "BK001"
	if boardType == "concept" {
		boardCode = "BK101"
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/boards/"+boardType+"/"+boardCode+"/quotes?as_of=2026-09-14", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload boardQuotePayload
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

type warmingQuoteSource struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (s *warmingQuoteSource) FetchStockQuotes(ctx context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	result := make([]graymarket.StockQuote, 0, len(relations))
	for _, relation := range relations {
		result = append(result, graymarket.StockQuote{StockCode: relation.StockCode, LatestPrice: 12.34, ChangePct: -0.015,
			Turnover: 12345678, Available: true, QuoteTime: "2026-09-14T10:00:00+08:00"})
	}
	return result, nil
}

func TestBoardQuoteWarmupCompletesWithLatestPricesForBothBoardTypes(t *testing.T) {
	for _, boardType := range []string{"industry", "concept"} {
		t.Run(boardType, func(t *testing.T) {
			server, store := testServer(t, "")
			defer store.Close()
			seedQuoteRelations(t, store)
			source := &warmingQuoteSource{started: make(chan struct{}), release: make(chan struct{})}
			defer func() {
				select {
				case <-source.release:
				default:
					close(source.release)
				}
			}()
			server.quotes = quote.NewCache(source, nil)
			first := getBoardQuotes(t, server, boardType)
			if first.Meta.QuoteStatus != "warming" || !first.Meta.QuoteRefreshing || first.Meta.QuoteAvailable || len(first.Data) != 2 {
				t.Fatalf("first request must signal background work and retain relations: %+v", first)
			}
			select {
			case <-source.started:
			case <-time.After(time.Second):
				t.Fatal("opening the board did not query the quote source")
			}
			for i := 0; i < 3; i++ {
				if next := getBoardQuotes(t, server, boardType); !next.Meta.QuoteRefreshing {
					t.Fatal("refresh signal ended before quotes arrived")
				}
			}
			if calls := source.calls.Load(); calls != 1 {
				t.Fatalf("warmup polls started duplicate upstream calls: %d", calls)
			}
			close(source.release)
			deadline := time.Now().Add(time.Second)
			for {
				ready := getBoardQuotes(t, server, boardType)
				if !ready.Meta.QuoteRefreshing {
					if ready.Meta.QuoteStatus != "ready" || !ready.Meta.QuoteAvailable || ready.Meta.QuotedCount != 2 || ready.Meta.DarkDataAvailable {
						t.Fatalf("unexpected ready meta: %+v", ready.Meta)
					}
					for _, row := range ready.Data {
						if !row.QuoteAvailable || row.LatestPrice != 12.34 || row.ChangePct != -0.015 || row.Turnover != 12345678 || row.DarkDataAvailable {
							t.Fatalf("latest quote fields not populated: %+v", row)
						}
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("warmup did not complete")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestBoardQuoteDarkAvailabilityRequiresCollectedMoney(t *testing.T) {
	for _, tc := range []struct {
		name  string
		money []bool
		count int
	}{
		{"no close archive", nil, 0},
		{"quote-only close", []bool{false, false}, 0},
		{"partially collected money", []bool{true, false}, 1},
		{"collected zero money", []bool{true, true}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := testServer(t, "")
			defer store.Close()
			seedQuoteRelations(t, store)
			if tc.money != nil {
				at := time.Date(2026, 9, 14, 15, 0, 0, 0, server.location)
				var records []graymarket.RankRecord
				for index, code := range []string{"000001", "000002"} {
					records = append(records, graymarket.RankRecord{TradeDate: "2026-09-14", SnapshotAt: at, RankType: graymarket.RankStock,
						Code: code, Rank: int64(index + 1), QuoteAvailable: true, MoneyAvailable: tc.money[index], FetchedAt: at})
				}
				if err := store.SaveDailyClose(context.Background(), "quotes-close", graymarket.RankSnapshot{TradeDate: "2026-09-14", RankType: graymarket.RankStock, SnapshotAt: at, Records: records}); err != nil {
					t.Fatal(err)
				}
			}
			for _, boardType := range []string{"industry", "concept"} {
				payload := getBoardQuotes(t, server, boardType)
				if payload.Meta.DarkDataAvailable != (tc.count > 0) || payload.Meta.DarkDataCount != tc.count || payload.Meta.QuoteRefreshing {
					t.Fatalf("incorrect availability: %+v", payload.Meta)
				}
				for index, row := range payload.Data {
					want := len(tc.money) > index && tc.money[index]
					if row.DarkDataAvailable != want || (!want && row.DarkRank != 0) {
						t.Fatalf("quote-only row exposed dark fields: %+v", row)
					}
				}
			}
		})
	}
}

type failedRefreshSnapshot struct{}

func (failedRefreshSnapshot) Snapshot(graymarket.BoardType, string, []graymarket.StockBoardRelation) (quote.Snapshot, quote.Status) {
	return quote.Snapshot{Quotes: []graymarket.StockQuote{{StockCode: "000001", Available: true, LatestPrice: 10, ChangePct: 0, Turnover: 0}}, Error: "upstream timeout"}, quote.StatusStale
}

func TestBoardQuoteRefreshFailureRetainsUsableCachedQuotes(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	seedQuoteRelations(t, store)
	server.quotes = failedRefreshSnapshot{}
	at := time.Date(2026, 9, 14, 15, 0, 0, 0, server.location)
	if err := store.SaveDailyClose(context.Background(), "old-close", graymarket.RankSnapshot{TradeDate: "2026-09-14", RankType: graymarket.RankStock, SnapshotAt: at,
		Records: []graymarket.RankRecord{{TradeDate: "2026-09-14", SnapshotAt: at, RankType: graymarket.RankStock, Code: "000001", Rank: 1,
			QuoteAvailable: true, MoneyAvailable: true, Turnover: 999999, FetchedAt: at}},
	}); err != nil {
		t.Fatal(err)
	}
	payload := getBoardQuotes(t, server, "industry")
	if !payload.Meta.QuoteAvailable || payload.Meta.QuotedCount != 1 || payload.Meta.QuoteError == "" || payload.Meta.QuoteRefreshing {
		t.Fatalf("failed refresh hid cached quotes or requested endless polling: %+v", payload.Meta)
	}
	if !payload.Data[0].QuoteAvailable || payload.Data[0].LatestPrice != 10 || payload.Data[0].Turnover != 0 {
		t.Fatalf("live zero turnover must not be replaced with archive turnover: %+v", payload.Data[0])
	}
}
