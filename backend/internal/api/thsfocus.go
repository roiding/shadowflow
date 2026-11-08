package api

import (
	"context"
	"net/http"

	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/thsfocus"
)

type thsFocusReader interface {
	ReadThsFocus(context.Context, string, string) (*thsfocus.Snapshot, error)
}

func (server *Server) thsFocus(response http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	for key, entries := range values {
		if key != "trade_date" && key != "scope" || len(entries) != 1 {
			writeError(response, http.StatusBadRequest, "invalid_ths_focus_request", "unsupported or repeated query parameter")
			return
		}
	}
	date, valid := dateParam(response, values.Get("trade_date"))
	if !valid {
		return
	}
	scope := values.Get("scope")
	if scope == "" {
		scope = limitup.ScopeDefault
	}
	if !limitup.ValidScope(scope) {
		writeError(response, http.StatusBadRequest, "invalid_ths_focus_request", "unsupported market scope")
		return
	}
	reader, available := server.store.(thsFocusReader)
	if !available {
		writeError(response, http.StatusServiceUnavailable, "ths_focus_unavailable", "同花顺补充复盘读取不可用")
		return
	}
	snapshot, err := reader.ReadThsFocus(request.Context(), date, scope)
	if err != nil {
		server.internalError(response, err)
		return
	}
	status := "not_collected"
	if snapshot != nil {
		status = "complete"
	}
	writeJSON(response, http.StatusOK, envelope{Data: struct {
		TradeDate string             `json:"trade_date"`
		Scope     string             `json:"scope"`
		Status    string             `json:"status"`
		Snapshot  *thsfocus.Snapshot `json:"snapshot"`
	}{date, scope, status, snapshot}})
}
