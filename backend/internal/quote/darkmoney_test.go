package quote

import (
	"context"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type liveDarkMoneyTestSource struct {
	records []graymarket.RankRecord
	points  []graymarket.MoneyPoint
}

func (source *liveDarkMoneyTestSource) FetchLatestMoney5m(
	_ context.Context,
	_ string,
	records []graymarket.RankRecord,
	_ time.Time,
) ([]graymarket.MoneyPoint, error) {
	source.records = append([]graymarket.RankRecord(nil), records...)
	return source.points, nil
}

func TestLimitUpLiveDarkMoneySourceDeduplicatesCandidates(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 42, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	upstream := &liveDarkMoneyTestSource{points: []graymarket.MoneyPoint{{
		Code: "001389", DarkMoney: 12_000_000, SnapshotAt: at,
	}}}
	source := NewLimitUpLiveDarkMoneySource(upstream)
	result, err := source.ReadLiveDarkMoney(
		context.Background(), "2026-11-11", []string{"001389", "001389", "bad", "000001"}, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.records) != 2 || upstream.records[0].Code != "001389" ||
		upstream.records[0].Market != 0 || upstream.records[1].Code != "000001" {
		t.Fatalf("unexpected dark-money records: %+v", upstream.records)
	}
	if result["001389"].DarkMoney != 12_000_000 || !result["001389"].Available {
		t.Fatalf("unexpected dark-money projection: %+v", result)
	}
}
