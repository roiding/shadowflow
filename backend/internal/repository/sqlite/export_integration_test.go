package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResearchExportFromRealSealedArchive(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("CSV export integration requires python3")
	}
	directory := t.TempDir()
	database := filepath.Join(directory, "archive #? with 'quote.db")
	store, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	completeArchiveFixture(t, store, "2026-09-02", 1, true)
	completeArchiveFixture(t, store, "2026-09-03", 1, true)
	completeArchiveFixture(t, store, "2026-09-04", 1, false)
	if _, err := store.db.Exec(`UPDATE daily_archive_manifest SET updated_at='2026-09-07T01:02:03Z' WHERE trade_date='2026-09-02'`); err != nil {
		t.Fatal(err)
	}
	revision, err := store.SealArchiveRevision(ctx, "2026-09-02", "retry")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "export")
	script, err := filepath.Abs("../../../../scripts/export_research.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), python, script, "--database", database, "--output", output,
		"--from-date", "2026-09-02", "--to-date", "2026-09-04", "--format", "csv", "--batch-size", "1")
	if body, err := command.CombinedOutput(); err != nil {
		t.Fatalf("export failed: %v\n%s", err, body)
	}
	body, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Revisions []struct {
			ContentSHA256 string `json:"content_sha256"`
		} `json:"revisions"`
		ExcludedDates []string                      `json:"excluded_dates"`
		Datasets      map[string]struct{ Rows int } `json:"datasets"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Revisions) != 2 || manifest.Revisions[0].ContentSHA256 != revision.ContentSHA256 ||
		len(manifest.ExcludedDates) != 1 || manifest.ExcludedDates[0] != "2026-09-04" {
		t.Fatalf("unexpected exported revision scope: %s", body)
	}
	for dataset, count := range map[string]int{"daily_close": 6, "daily_features": 6, "future_labels": 3,
		"board_money_5m": 192, "stock_research_5m": 96} {
		if manifest.Datasets[dataset].Rows != count {
			t.Errorf("%s: got %d rows, want %d", dataset, manifest.Datasets[dataset].Rows, count)
		}
	}
}
