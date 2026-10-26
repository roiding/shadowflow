package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func TestHistoricalMinuteAPIsReadColdStorage(t *testing.T) {
	root := t.TempDir()
	coldPath := filepath.Join(root, "cold.db")
	cold, err := sqlite.Open(coldPath)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 9, 36, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	record := graymarket.RankRecord{TradeDate: "2026-10-09", SnapshotAt: at,
		RankType: graymarket.RankIndustry, Market: 90, Code: "BK0448", Rank: 1,
		Name: "bank", DarkMoney: 123, MoneyAvailable: true, FetchedAt: at}
	if err := cold.SaveIntraday(context.Background(), "cold-run", graymarket.RankSnapshot{
		TradeDate: record.TradeDate, RankType: record.RankType, SnapshotAt: at,
		Records: []graymarket.RankRecord{record}}, false); err != nil {
		t.Fatal(err)
	}
	if err := cold.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.OpenWithCold(filepath.Join(root, "main.db"), coldPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calendar, err := tradingcalendar.Load(filepath.Join(root, "calendar.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store, calendar, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/boards/industry/BK0448/intraday?trade_date=2026-10-09",
		"/api/v1/ranks?type=industry&trade_date=2026-10-09&at=09:36",
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var body struct {
			Data []graymarket.RankRecord `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(body.Data) != 1 || body.Data[0].DarkMoney != 123 || !body.Data[0].MoneyAvailable {
			t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
