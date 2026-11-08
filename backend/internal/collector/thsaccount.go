package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/datasource/thsdataq"
	"github.com/roiding/shadowflow/internal/limitup"
)

// THSAccountSource fetches restricted THS stock data for explicit market:code
// identities (17: SH A-share, 33: SZ A-share) using an authenticated session.
type THSAccountSource interface {
	Fetch(ctx context.Context, tradeDate string, identities []string) (json.RawMessage, error)
}

type thsAccountStore interface {
	ReadLimitUpWindow(ctx context.Context, fromDate, asOf, scope string) (limitup.Dataset, error)
	SaveThsAccountData(ctx context.Context, tradeDate, identity, payload string, fetchedAt time.Time) error
	ThsAccountDataPayloads(ctx context.Context, tradeDate string, identities []string) (map[string]string, error)
}

const thsAccountBatchSize = 20

func (service *Service) WithThsAccountSource(source THSAccountSource) *Service {
	service.thsAccountSource = source
	return service
}

// CollectThsAccountData archives the restricted THS dataset (limit-up streaks,
// seal times, reasons and turnover) for every stock in the day's limit-up
// event archive. A missing event archive skips the run so the scheduled slots
// can retry after the events lane has landed; an already complete archive is
// also skipped so replay slots stay idempotent.
func (service *Service) CollectThsAccountData(ctx context.Context, runAt time.Time) error {
	if service.thsAccountSource == nil {
		return fmt.Errorf("THS account data source is not configured")
	}
	store, available := service.store.(thsAccountStore)
	if !available {
		return fmt.Errorf("THS account data store is not configured")
	}
	tradeDate := runAt.In(limitup.Shanghai()).Format("2006-01-02")
	dataset, err := store.ReadLimitUpWindow(ctx, tradeDate, tradeDate, service.limitUpScope)
	if err != nil {
		return err
	}
	identities := thsIdentities(dataset.Events)
	if len(identities) == 0 {
		return nil
	}
	stored, err := store.ThsAccountDataPayloads(ctx, tradeDate, identities)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(identities))
	for _, identity := range identities {
		if !thsdataq.RecordComplete(stored[identity], tradeDate, identity) {
			missing = append(missing, identity)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	fetchedAt := time.Now().UTC()
	for start := 0; start < len(missing); start += thsAccountBatchSize {
		end := start + thsAccountBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		batch := missing[start:end]
		payload, err := service.thsAccountSource.Fetch(ctx, tradeDate, batch)
		if err != nil {
			return err
		}
		if err := StoreThsAccountBatch(ctx, store, tradeDate, batch, payload, fetchedAt); err != nil {
			return err
		}
	}
	return nil
}

// thsIdentities maps limit-up events to THS restricted-data identities. The
// event's own market id wins when it is consistent with the code prefix;
// otherwise the prefix decides and unsupported markets (BSE) are skipped.
func thsIdentities(events []limitup.Event) []string {
	out := make([]string, 0, len(events))
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		identity := thsIdentity(event)
		if identity == "" || seen[identity] {
			continue
		}
		seen[identity] = true
		out = append(out, identity)
	}
	sort.Strings(out)
	return out
}

func thsIdentity(event limitup.Event) string {
	code := strings.TrimSpace(event.Code)
	if len(code) != 6 {
		return ""
	}
	var market string
	switch {
	case strings.HasPrefix(code, "60"), strings.HasPrefix(code, "68"):
		market = "17"
	case strings.HasPrefix(code, "00"), strings.HasPrefix(code, "30"):
		market = "33"
	default:
		return ""
	}
	if (event.MarketID == 17 && market == "17") || (event.MarketID == 33 && market == "33") {
		return fmt.Sprintf("%d:%s", event.MarketID, code)
	}
	return market + ":" + code
}

// ThsAccountSaveStore archives one restricted THS record per stock identity.
type ThsAccountSaveStore interface {
	SaveThsAccountData(ctx context.Context, tradeDate, identity, payload string, fetchedAt time.Time) error
}

// StoreThsAccountResponse splits a raw thsdataq batch response into per-stock
// rows and archives them, so manual CLI runs and the scheduled lane persist
// the same shape.
func StoreThsAccountResponse(ctx context.Context, store ThsAccountSaveStore, tradeDate string, payload json.RawMessage, fetchedAt time.Time) error {
	records, err := thsdataq.ParseStoredResponse(payload, tradeDate)
	if err != nil {
		return err
	}
	return storeThsAccountRecords(ctx, store, records, fetchedAt)
}

func StoreThsAccountBatch(ctx context.Context, store ThsAccountSaveStore, tradeDate string, identities []string, payload json.RawMessage, fetchedAt time.Time) error {
	records, err := thsdataq.ParseResponse(payload, tradeDate, identities)
	if err != nil {
		return err
	}
	return storeThsAccountRecords(ctx, store, records, fetchedAt)
}

func storeThsAccountRecords(ctx context.Context, store ThsAccountSaveStore, records []thsdataq.StockRecord, fetchedAt time.Time) error {
	if len(records) == 0 {
		return fmt.Errorf("THS account data response contains no rows")
	}
	for _, record := range records {
		identity := strings.TrimSpace(record.Code)
		if !validThsIdentity(identity) {
			return fmt.Errorf("THS account data response has unexpected code %q", identity)
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode THS account record %s: %w", identity, err)
		}
		if !thsdataq.RecordComplete(string(payload), record.TradeDate, identity) {
			return fmt.Errorf("THS account data record %s is incomplete", identity)
		}
		if err := store.SaveThsAccountData(ctx, record.TradeDate, identity, string(payload), fetchedAt); err != nil {
			return err
		}
	}
	return nil
}

func validThsIdentity(identity string) bool {
	market, code, found := strings.Cut(identity, ":")
	if !found || (market != "17" && market != "33") || len(code) != 6 {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
