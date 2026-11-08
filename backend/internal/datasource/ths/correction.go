package ths

import "github.com/roiding/shadowflow/internal/limitup"

func applyCountCorrection(snapshot *limitup.Snapshot, summaries dailySummaries) error {
	correction, correctionError := limitup.ReconcileCountCorrection(*snapshot, summaries.Up.OpenNum, summaries.Up.HistoryNum)
	if correctionError != nil {
		return correctionError
	}
	snapshot.CountCorrection = correction
	return nil
}
