package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/roiding/shadowflow/internal/limitup"
)

func limitUpQuery(request *http.Request) (limitup.Query, error) {
	values := request.URL.Query()
	allowed := map[string]bool{"as_of": true, "days": true, "scope": true, "event": true, "min_overlap": true, "limit": true, "order": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return limitup.Query{}, fmt.Errorf("%w: unsupported or repeated query parameter", limitup.ErrInvalid)
		}
	}
	query := limitup.Query{AsOf: values.Get("as_of"), Scope: values.Get("scope"), Event: values.Get("event"),
		Order: strings.TrimSpace(values.Get("order"))}
	for key, target := range map[string]*int{"days": &query.Days, "min_overlap": &query.MinOverlap, "limit": &query.Limit} {
		if !values.Has(key) {
			continue
		}
		parsed, err := strconv.Atoi(values.Get(key))
		if err != nil || parsed < 1 {
			return limitup.Query{}, fmt.Errorf("%w: %s must be a positive integer", limitup.ErrInvalid, key)
		}
		*target = parsed
	}
	query, _, err := limitup.NormalizeQuery(query)
	return query, err
}

func (server *Server) limitUpOverview(response http.ResponseWriter, request *http.Request) {
	query, err := limitUpQuery(request)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	reader, available := server.store.(limitup.Reader)
	if !available {
		writeError(response, http.StatusServiceUnavailable, "limit_up_unavailable", "事件数据读取暂不可用")
		return
	}
	result, err := limitup.New(reader).Overview(request.Context(), query)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, envelope{Data: result})
}

func (server *Server) limitUpStock(response http.ResponseWriter, request *http.Request) {
	query, err := limitUpQuery(request)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	code := chi.URLParam(request, "code")
	if !limitup.ValidCode(code) {
		writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "股票代码须为六位数字")
		return
	}
	reader, available := server.store.(limitup.Reader)
	if !available {
		writeError(response, http.StatusServiceUnavailable, "limit_up_unavailable", "事件数据读取暂不可用")
		return
	}
	result, err := limitup.New(reader).Analyze(request.Context(), query, code)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, envelope{Data: result})
}

func (server *Server) limitUpTodayNetwork(response http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	allowed := map[string]bool{"days": true, "scope": true, "min_overlap": true, "top": true, "candidates": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "unsupported or repeated query parameter")
			return
		}
	}
	days, minOverlap, top, candidates := 365, 1, 5, 20
	for key, target := range map[string]*int{"days": &days, "min_overlap": &minOverlap, "top": &top, "candidates": &candidates} {
		if value := values.Get(key); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 {
				writeError(response, http.StatusBadRequest, "invalid_limit_up_request", key+" must be a positive integer")
				return
			}
			*target = parsed
		}
	}
	if days > 366 || minOverlap > 20 || top > 20 || candidates > 50 {
		writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "days must be 1..366, min_overlap 1..20, top 1..20, candidates 1..50")
		return
	}
	scope := values.Get("scope")
	if scope == "" {
		scope = server.limitUpScope
		if scope == "" {
			scope = limitup.ScopeDefault
		}
	}
	if !limitup.ValidScope(scope) {
		writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "unsupported event scope")
		return
	}
	if server.limitUpIntraday == nil {
		writeError(response, http.StatusServiceUnavailable, "limit_up_intraday_unavailable", "盘中涨停快照数据源暂不可用")
		return
	}
	if server.limitUpHistoryDays == nil {
		writeError(response, http.StatusServiceUnavailable, "limit_up_history_unavailable", "历史涨停归档读取暂不可用")
		return
	}
	snapshot, err := server.limitUpIntraday.FetchSealedToday(request.Context(), scope)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	historyAsOf, err := server.limitUpHistoryDays.LatestLimitUpDay(request.Context(), snapshot.TradeDate, scope)
	if err != nil {
		server.internalError(response, err)
		return
	}
	snapshot.ArchivedThrough = historyAsOf
	history := limitup.Dataset{Days: []limitup.Day{}, Events: []limitup.Event{}}
	historyFrom := ""
	if historyAsOf != "" {
		asOf, _ := time.ParseInLocation("2006-01-02", historyAsOf, server.location)
		historyFrom = asOf.AddDate(0, 0, 1-days).Format("2006-01-02")
		reader, available := server.store.(limitup.Reader)
		if !available {
			writeError(response, http.StatusServiceUnavailable, "limit_up_history_unavailable", "历史涨停归档读取暂不可用")
			return
		}
		history, err = reader.ReadLimitUpWindow(request.Context(), historyFrom, historyAsOf, scope)
		if err != nil {
			server.limitUpError(response, err)
			return
		}
	}
	result, err := limitup.AnalyzeToday(request.Context(), limitup.TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: historyFrom,
		LookbackDays: days, MinOverlap: minOverlap, TopCores: top, CandidateLimit: candidates,
	})
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, envelope{Data: result, Meta: map[string]any{
		"snapshot_only": true, "history_excludes_today": true,
		"snapshot_fetched_at": snapshot.FetchedAt,
	}})
}

func (server *Server) limitUpError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, limitup.ErrInvalid):
		writeError(response, http.StatusBadRequest, "invalid_limit_up_request", err.Error())
	case errors.Is(err, limitup.ErrMissing):
		writeError(response, http.StatusNotFound, "limit_up_stock_not_found", "所选范围内没有该股票的已完整采集事件")
	default:
		server.internalError(response, err)
	}
}

func (server *Server) limitUpQuality(response http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	for key, entries := range values {
		if key != "trade_date" && key != "scope" || len(entries) != 1 {
			writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "unsupported or repeated query parameter")
			return
		}
	}
	tradeDate, valid := dateParam(response, values.Get("trade_date"))
	if !valid {
		return
	}
	configuredScope := server.limitUpScope
	if configuredScope == "" {
		configuredScope = limitup.ScopeDefault
	}
	scope := values.Get("scope")
	if scope == "" {
		scope = configuredScope
	}
	if !limitup.ValidScope(scope) {
		writeError(response, http.StatusBadRequest, "invalid_limit_up_request", "unsupported event scope")
		return
	}
	reader, available := server.store.(limitup.QualityReader)
	if !available {
		writeError(response, http.StatusServiceUnavailable, "limit_up_unavailable", "事件归档状态暂不可用")
		return
	}
	quality, err := reader.LimitUpArchiveQuality(request.Context(), tradeDate, scope)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	quality.Scheduled = server.limitUpArchiveScheduled && scope == configuredScope
	if !quality.Scheduled && quality.Status == limitup.ArchiveStatusNotCollected {
		quality.Status = limitup.ArchiveStatusDisabled
	}
	writeJSON(response, http.StatusOK, envelope{Data: quality})
}

func limitUpConcurrencyGate() func(http.Handler) http.Handler {
	slots := make(chan struct{}, 1)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				next.ServeHTTP(response, request)
			default:
				response.Header().Set("Retry-After", "1")
				writeError(response, http.StatusServiceUnavailable, "limit_up_busy", "股票研究正在计算，请稍后重试")
			}
		})
	}
}
