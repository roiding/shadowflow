package limitup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateSnapshotUsesTheClientPageBudget(t *testing.T) {
	rawPages := func(count int) []json.RawMessage {
		pages := make([]json.RawMessage, count)
		for index := range pages {
			pages[index] = json.RawMessage(`{}`)
		}
		return pages
	}
	snapshot := Snapshot{
		TradeDate: "2026-08-03", Source: SourceTHS, Scope: ScopeDefault,
		FetchedAt: time.Date(2026, 8, 3, 16, 0, 0, 0, Shanghai()), RawPages: rawPages(MaxSourcePages),
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("maximum client page budget was rejected: %v", err)
	}
	snapshot.RawPages = rawPages(MaxSourcePages + 1)
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("source page evidence beyond the client budget was accepted")
	}
}
