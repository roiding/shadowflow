package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

type Server struct {
	store    repository.Store
	quotes   StockQuoteSource
	calendar *tradingcalendar.Calendar
	logger   *slog.Logger
	location *time.Location
	started  time.Time
	router   chi.Router
}

type Options struct {
	StaticDir   string
	QuoteSource StockQuoteSource
}

type StockQuoteSource interface {
	FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error)
}

type envelope struct {
	Data  any       `json:"data,omitempty"`
	Meta  any       `json:"meta,omitempty"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(store repository.Store, calendar *tradingcalendar.Calendar, logger *slog.Logger, options ...Options) (*Server, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	server := &Server{store: store, calendar: calendar, logger: logger, location: location, started: time.Now().UTC()}
	if len(options) > 0 {
		server.quotes = options[0].QuoteSource
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Compress(5))
	router.Get("/health/live", server.live)
	router.Get("/health/ready", server.ready)
	router.Get("/metrics", server.metrics)
	router.Route("/api/v1", func(r chi.Router) {
		r.Get("/ranks/latest", server.latestRank)
		r.Get("/ranks", server.rankAt)
		r.Get("/ranks/daily-close", server.dailyClose)
		r.Get("/trading-days", server.tradingDays)
		r.Get("/boards/{type}/{code}/intraday", server.intraday)
		r.Get("/boards/{type}/{code}/trend", server.trend)
		r.Get("/boards/{type}/{code}/stocks", server.boardStocks)
		r.Get("/boards/{type}/{code}/quotes", server.boardQuotes)
		r.Get("/stocks/{code}/boards", server.stockBoards)
		r.Get("/relations/changes", server.relationChanges)
		r.Get("/research/export", server.exportResearch)
		r.Get("/research/daily-close/export", server.exportDailyClose)
		r.Get("/research/quality", server.quality)
		r.Get("/collection-runs", server.collectionRuns)
		r.Get("/system/status", server.status)
	})
	if len(options) > 0 && options[0].StaticDir != "" {
		mountStatic(router, options[0].StaticDir)
	}
	server.router = router
	return server, nil
}

func (s *Server) stockBoards(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if !stockCodeValid(code) {
		writeError(w, http.StatusBadRequest, "invalid_stock_code", "stock code must contain exactly 6 digits")
		return
	}
	asOf, ok := optionalAsOf(w, r, s.location)
	if !ok {
		return
	}
	relations, err := s.store.StockBoardRelations(r.Context(), code, asOf)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: relations, Meta: map[string]any{
		"as_of": asOf, "stock_code": code, "relation_source": graymarket.RelationSourceQuoteClist,
		"relation_scope": graymarket.RelationScopeBoardConstituents,
	}})
}

func (s *Server) boardStocks(w http.ResponseWriter, r *http.Request) {
	boardType, ok := relationBoardTypeParam(w, chi.URLParam(r, "type"))
	if !ok {
		return
	}
	boardCode := chi.URLParam(r, "code")
	if !strings.HasPrefix(boardCode, "BK") || len(boardCode) < 4 {
		writeError(w, http.StatusBadRequest, "invalid_board_code", "board code must use the BK prefix")
		return
	}
	asOf, ok := optionalAsOf(w, r, s.location)
	if !ok {
		return
	}
	relations, err := s.store.BoardStockRelations(r.Context(), boardType, boardCode, asOf)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: relations, Meta: map[string]any{
		"as_of": asOf, "board_type": boardType, "board_code": boardCode,
		"relation_source": graymarket.RelationSourceQuoteClist, "relation_scope": graymarket.RelationScopeBoardConstituents,
	}})
}

type boardStockQuote struct {
	StockCode         string               `json:"stock_code"`
	StockMarket       int64                `json:"stock_market"`
	StockName         string               `json:"stock_name"`
	BoardCode         string               `json:"board_code"`
	BoardName         string               `json:"board_name"`
	BoardType         graymarket.BoardType `json:"board_type"`
	SourceOrder       int                  `json:"source_order"`
	EffectiveDate     string               `json:"effective_date,omitempty"`
	LatestPrice       float64              `json:"latest_price"`
	OpenPrice         float64              `json:"open_price"`
	HighPrice         float64              `json:"high_price"`
	LowPrice          float64              `json:"low_price"`
	PreviousClose     float64              `json:"previous_close"`
	ChangePct         float64              `json:"change_pct"`
	ChangeValue       float64              `json:"change_value"`
	Volume            int64                `json:"volume"`
	Turnover          int64                `json:"turnover"`
	TurnoverRate      float64              `json:"turnover_rate"`
	Amplitude         float64              `json:"amplitude"`
	QuoteTime         string               `json:"quote_time"`
	FetchedAt         time.Time            `json:"fetched_at,omitempty"`
	QuoteAvailable    bool                 `json:"quote_available"`
	DarkRank          int64                `json:"dark_rank"`
	DarkMoney         int64                `json:"dark_money"`
	MainMoneyInflow   int64                `json:"main_money_inflow"`
	DarkActivity      float64              `json:"dark_activity"`
	DarkDataAvailable bool                 `json:"dark_data_available"`
}

func (s *Server) boardQuotes(w http.ResponseWriter, r *http.Request) {
	boardType, ok := relationBoardTypeParam(w, chi.URLParam(r, "type"))
	if !ok {
		return
	}
	boardCode := chi.URLParam(r, "code")
	if !strings.HasPrefix(boardCode, "BK") || len(boardCode) < 4 {
		writeError(w, http.StatusBadRequest, "invalid_board_code", "board code must use the BK prefix")
		return
	}
	asOf, ok := optionalAsOf(w, r, s.location)
	if !ok {
		return
	}
	relations, err := s.store.BoardStockRelations(r.Context(), boardType, boardCode, asOf)
	if err != nil {
		s.internalError(w, err)
		return
	}
	stockCodes := make([]string, 0, len(relations))
	for _, relation := range relations {
		stockCodes = append(stockCodes, relation.StockCode)
	}
	darkRecords, err := s.store.DailyCloseStocks(r.Context(), asOf, stockCodes)
	if err != nil {
		s.internalError(w, err)
		return
	}
	darkByCode := make(map[string]graymarket.RankRecord, len(darkRecords))
	for _, record := range darkRecords {
		darkByCode[record.Code] = record
	}

	quotes := make(map[string]graymarket.StockQuote, len(relations))
	meta := map[string]any{
		"as_of": asOf, "board_type": boardType, "board_code": boardCode,
		"quote_source": "unavailable", "quote_available": false,
		"dark_data_available": len(darkRecords) > 0, "dark_data_count": len(darkRecords),
	}
	if s.quotes != nil && len(relations) > 0 {
		latest, quoteErr := s.quotes.FetchStockQuotes(r.Context(), relations)
		if quoteErr != nil {
			meta["quote_error"] = quoteErr.Error()
		} else {
			availableCount := 0
			for _, quote := range latest {
				quotes[quote.StockCode] = quote
				if quote.Available {
					availableCount++
				}
			}
			meta["quote_source"] = "eastmoney"
			meta["quote_available"] = availableCount > 0
			meta["quoted_count"] = availableCount
		}
	}
	result := make([]boardStockQuote, 0, len(relations))
	for _, relation := range relations {
		quote := quotes[relation.StockCode]
		dark, darkAvailable := darkByCode[relation.StockCode]
		turnover := quote.Turnover
		if turnover == 0 {
			turnover = dark.Turnover
		}
		openPrice, highPrice, lowPrice, previousClose := quote.OpenPrice, quote.HighPrice, quote.LowPrice, quote.PreviousClose
		turnoverRate, amplitude := quote.TurnoverRate, quote.Amplitude
		if openPrice == 0 {
			openPrice, highPrice, lowPrice, previousClose = dark.OpenPrice, dark.HighPrice, dark.LowPrice, dark.PreviousClose
			turnoverRate, amplitude = dark.TurnoverRate, dark.Amplitude
		}
		result = append(result, boardStockQuote{
			StockCode: relation.StockCode, StockMarket: relation.StockMarket, StockName: relation.StockName,
			BoardCode: relation.BoardCode, BoardName: relation.BoardName, BoardType: relation.BoardType,
			SourceOrder: relation.SourceOrder, EffectiveDate: relation.EffectiveDate,
			LatestPrice: quote.LatestPrice, OpenPrice: openPrice, HighPrice: highPrice, LowPrice: lowPrice, PreviousClose: previousClose,
			ChangePct: quote.ChangePct, ChangeValue: quote.ChangeValue, Volume: quote.Volume, Turnover: turnover,
			TurnoverRate: turnoverRate, Amplitude: amplitude, QuoteTime: quote.QuoteTime, FetchedAt: quote.FetchedAt,
			QuoteAvailable: quote.Available,
			DarkRank:       dark.Rank, DarkMoney: dark.DarkMoney, MainMoneyInflow: dark.MainMoneyInflow,
			DarkActivity: dark.DarkActivity, DarkDataAvailable: darkAvailable,
		})
	}
	writeJSON(w, http.StatusOK, envelope{Data: result, Meta: meta})
}

func (s *Server) relationChanges(w http.ResponseWriter, r *http.Request) {
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	var boardType graymarket.BoardType
	if value := r.URL.Query().Get("type"); value != "" {
		boardType, ok = relationBoardTypeParam(w, value)
		if !ok {
			return
		}
	}
	changes, err := s.store.RelationChanges(r.Context(), tradeDate, boardType)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: changes, Meta: map[string]any{"trade_date": tradeDate, "board_type": boardType}})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.store.OperationalMetrics(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writeMetricHelp(w, "shadowflow_collector_runs_total", "Collection runs grouped by rank type and status", "counter")
	for _, item := range metrics.RunCounts {
		fmt.Fprintf(w, "shadowflow_collector_runs_total{rank_type=%q,status=%q} %d\n", item.RankType, item.Status, item.Value)
	}
	writeMetricHelp(w, "shadowflow_collector_records_total", "Successfully collected records grouped by rank type", "counter")
	for _, item := range metrics.RecordCounts {
		fmt.Fprintf(w, "shadowflow_collector_records_total{rank_type=%q} %.0f\n", item.RankType, item.Value)
	}
	writeMetricHelp(w, "shadowflow_collector_request_duration_seconds", "Collection duration by rank type", "summary")
	for _, item := range metrics.DurationSecondsSum {
		fmt.Fprintf(w, "shadowflow_collector_request_duration_seconds_sum{rank_type=%q} %.3f\n", item.RankType, item.Value)
	}
	for _, item := range metrics.DurationCounts {
		fmt.Fprintf(w, "shadowflow_collector_request_duration_seconds_count{rank_type=%q} %.0f\n", item.RankType, item.Value)
	}
	writeMetricHelp(w, "shadowflow_collector_last_success_timestamp_seconds", "Unix timestamp of the last successful collection", "gauge")
	for _, item := range metrics.LastSuccess {
		fmt.Fprintf(w, "shadowflow_collector_last_success_timestamp_seconds{rank_type=%q} %d\n", item.RankType, item.Value.Unix())
	}
	writeMetricHelp(w, "shadowflow_intraday_latest_snapshot_timestamp_seconds", "Unix timestamp of the latest board snapshot", "gauge")
	for _, item := range metrics.LatestIntradaySnapshot {
		fmt.Fprintf(w, "shadowflow_intraday_latest_snapshot_timestamp_seconds{rank_type=%q} %d\n", item.RankType, item.Value.Unix())
	}
	writeMetricHelp(w, "shadowflow_research_compaction_runs_total", "Trading dates with completed research compaction", "counter")
	fmt.Fprintf(w, "shadowflow_research_compaction_runs_total %d\n", metrics.ResearchCompactionRuns)
	writeMetricHelp(w, "shadowflow_research_missing_snapshots_total", "Missing long-term research snapshots grouped by rank type", "gauge")
	for _, item := range metrics.ResearchMissingSnapshot {
		fmt.Fprintf(w, "shadowflow_research_missing_snapshots_total{rank_type=%q} %.0f\n", item.RankType, item.Value)
	}
}

func writeMetricHelp(w http.ResponseWriter, name, help, metricType string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func mountStatic(router chi.Router, directory string) {
	fileServer := http.FileServer(http.Dir(directory))
	router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health/") || r.URL.Path == "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.ServeFile(w, r, directory+"/index.html")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		candidate := filepath.Join(directory, filepath.FromSlash(path))
		relative, relErr := filepath.Rel(directory, candidate)
		withinRoot := relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
		if withinRoot {
			if _, err := os.Stat(candidate); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, directory+"/index.html")
	})
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, envelope{Data: map[string]any{"status": "ok"}})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	_, err := s.store.RecentRuns(ctx, time.Now().In(s.location).Format("2006-01-02"), 1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: map[string]any{"status": "ready"}})
}

func (s *Server) latestRank(w http.ResponseWriter, r *http.Request) {
	rankType, ok := boardTypeParam(w, r.URL.Query().Get("type"))
	if !ok {
		return
	}
	records, err := s.store.LatestRank(r.Context(), rankType)
	if err != nil {
		s.internalError(w, err)
		return
	}
	now := time.Now().In(s.location)
	meta := map[string]any{"rank_type": rankType, "count": len(records), "market_status": s.marketStatus(now)}
	if len(records) > 0 {
		meta["snapshot_at"] = records[0].SnapshotAt
		meta["trade_date"] = records[0].TradeDate
	}
	if len(records) > 0 && records[0].SnapshotAt.In(s.location).Format("15:04") == "15:00" {
		meta["snapshot_kind"] = graymarket.SnapshotDailyClose
	} else {
		meta["snapshot_kind"] = graymarket.SnapshotMinuteWork
	}
	writeJSON(w, http.StatusOK, envelope{Data: records, Meta: meta})
}

func (s *Server) rankAt(w http.ResponseWriter, r *http.Request) {
	rankType, ok := boardTypeParam(w, r.URL.Query().Get("type"))
	if !ok {
		return
	}
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	at := r.URL.Query().Get("at")
	if len(at) != 5 {
		writeError(w, http.StatusBadRequest, "invalid_at", "at must use HH:MM")
		return
	}
	snapshotAt, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+at, s.location)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_at", err.Error())
		return
	}
	records, err := s.store.RankAt(r.Context(), rankType, tradeDate, snapshotAt)
	if err != nil {
		s.internalError(w, err)
		return
	}
	snapshotKind := graymarket.SnapshotResearch5m
	if at == "15:00" {
		snapshotKind = graymarket.SnapshotDailyClose
	}
	writeJSON(w, http.StatusOK, envelope{Data: records, Meta: map[string]any{"count": len(records), "snapshot_at": snapshotAt, "snapshot_kind": snapshotKind}})
}

func (s *Server) intraday(w http.ResponseWriter, r *http.Request) {
	rankType, ok := boardTypeParam(w, chi.URLParam(r, "type"))
	if !ok {
		return
	}
	tradeDate := r.URL.Query().Get("trade_date")
	if tradeDate == "" {
		tradeDate = time.Now().In(s.location).Format("2006-01-02")
	}
	if _, ok := dateParam(w, tradeDate); !ok {
		return
	}
	records, err := s.store.IntradaySeries(r.Context(), rankType, chi.URLParam(r, "code"), tradeDate)
	if err != nil {
		s.internalError(w, err)
		return
	}
	interval := "1m"
	researchPoints, closePoints := 0, 0
	if len(records) > 0 {
		interval = "5m"
		for _, record := range records {
			isClose := record.SnapshotAt.In(s.location).Format("15:04") == "15:00"
			if record.SnapshotAt.Minute()%5 != 0 {
				interval = "1m"
			}
			if isClose {
				closePoints = 1
			} else if record.SnapshotAt.Minute()%5 == 0 {
				researchPoints++
			}
		}
		if interval == "5m" && closePoints == 1 {
			interval = "5m+close"
		}
	}
	writeJSON(w, http.StatusOK, envelope{Data: records, Meta: map[string]any{"count": len(records), "trade_date": tradeDate, "interval": interval, "research_points": researchPoints, "daily_close_points": closePoints}})
}

func (s *Server) trend(w http.ResponseWriter, r *http.Request) {
	rankType, ok := boardTypeParam(w, chi.URLParam(r, "type"))
	if !ok {
		return
	}
	from, to, ok := rangeParams(w, r, s.location)
	if !ok {
		return
	}
	records, err := s.store.ResearchSeries(r.Context(), rankType, chi.URLParam(r, "code"), from, to)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: records, Meta: map[string]any{"count": len(records), "interval": "5m", "from": from, "to": to}})
}

func (s *Server) dailyClose(w http.ResponseWriter, r *http.Request) {
	rankTypeRaw := r.URL.Query().Get("type")
	if rankTypeRaw == "" {
		rankTypeRaw = string(graymarket.RankStock)
	}
	rankType, err := graymarket.ParseRankType(rankTypeRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_type", "type must be industry, concept, or stock")
		return
	}
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	page, pageSize, ok := pageParams(w, r)
	if !ok {
		return
	}
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "rank"
	}
	allowedSort := map[string]bool{
		"rank": true, "name": true, "code": true, "dark_money": true, "main_money_inflow": true,
		"change_pct": true, "dark_activity": true, "open_price": true, "high_price": true,
		"low_price": true, "close_price": true, "previous_close": true, "volume": true,
		"turnover": true, "turnover_rate": true, "amplitude": true,
	}
	if !allowedSort[sort] {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort is not supported")
		return
	}
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		writeError(w, http.StatusBadRequest, "invalid_direction", "direction must be asc or desc")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(search)) > 50 {
		writeError(w, http.StatusBadRequest, "query_too_long", "q cannot exceed 50 characters")
		return
	}
	records, total, err := s.store.DailyClosePage(r.Context(), rankType, tradeDate, search, sort, direction == "desc", pageSize, (page-1)*pageSize)
	if err != nil {
		s.internalError(w, err)
		return
	}
	pages := 0
	if total > 0 {
		pages = (total + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, envelope{Data: records, Meta: map[string]any{"count": len(records), "total": total, "page": page, "page_size": pageSize, "pages": pages, "trade_date": tradeDate, "rank_type": rankType, "snapshot_kind": graymarket.SnapshotDailyClose}})
}

func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if r.URL.Query().Get("page") == "" {
		page = 1
	} else if err != nil || page < 1 {
		writeError(w, http.StatusBadRequest, "invalid_page", "page must be a positive integer")
		return 0, 0, false
	}
	pageSize, err := strconv.Atoi(r.URL.Query().Get("page_size"))
	if r.URL.Query().Get("page_size") == "" {
		pageSize = 100
	} else if err != nil || pageSize < 1 || pageSize > 200 {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "page_size must be between 1 and 200")
		return 0, 0, false
	}
	return page, pageSize, true
}

func (s *Server) tradingDays(w http.ResponseWriter, r *http.Request) {
	fromRaw, toRaw := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fromRaw == "" || toRaw == "" {
		writeError(w, http.StatusBadRequest, "invalid_range", "from and to are required")
		return
	}
	from, err := time.ParseInLocation("2006-01-02", fromRaw, s.location)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_from", "from must use YYYY-MM-DD")
		return
	}
	to, err := time.ParseInLocation("2006-01-02", toRaw, s.location)
	if err != nil || to.Before(from) {
		writeError(w, http.StatusBadRequest, "invalid_to", "to must use YYYY-MM-DD and be after from")
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "range_too_large", "date range cannot exceed 366 days")
		return
	}
	days := make([]string, 0)
	for current := from; !current.After(to); current = current.AddDate(0, 0, 1) {
		if s.calendar.IsTradingDay(current) {
			days = append(days, current.Format("2006-01-02"))
		}
	}
	writeJSON(w, http.StatusOK, envelope{Data: days, Meta: map[string]any{"from": fromRaw, "to": toRaw, "count": len(days)}})
}

func (s *Server) quality(w http.ResponseWriter, r *http.Request) {
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	result, err := s.store.Quality(r.Context(), tradeDate)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: result, Meta: map[string]any{"trade_date": tradeDate}})
}

func (s *Server) collectionRuns(w http.ResponseWriter, r *http.Request) {
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.RecentRuns(r.Context(), tradeDate, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Data: runs, Meta: map[string]any{"count": len(runs), "trade_date": tradeDate}})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().In(s.location)
	writeJSON(w, http.StatusOK, envelope{Data: map[string]any{
		"server_time": now, "timezone": "Asia/Shanghai", "market_status": s.marketStatus(now),
		"trading_day": s.calendar.IsTradingDay(now), "latest_trading_day": s.latestTradingDay(now),
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
	}})
}

func (s *Server) latestTradingDay(value time.Time) string {
	day := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, s.location)
	for !s.calendar.IsTradingDay(day) {
		day = day.AddDate(0, 0, -1)
	}
	return day.Format("2006-01-02")
}

func (s *Server) marketStatus(value time.Time) string {
	if !s.calendar.IsTradingDay(value) {
		return "closed"
	}
	return marketStatus(value)
}

func (s *Server) exportResearch(w http.ResponseWriter, r *http.Request) {
	rankType, ok := boardTypeParam(w, r.URL.Query().Get("type"))
	if !ok {
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "code is required")
		return
	}
	from, to, ok := rangeParams(w, r, s.location)
	if !ok {
		return
	}
	records, err := s.store.ResearchSeries(r.Context(), rankType, code, from, to)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shadowflow-%s-%s.csv"`, rankType, code))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"trade_date", "snapshot_at", "rank_type", "rank", "code", "name", "latest_price_raw", "change_pct", "dark_money", "regular_money", "main_money_inflow", "dark_activity", "dark_inflow_ratio", "up_count", "down_count"})
	for _, record := range records {
		_ = writer.Write([]string{record.TradeDate, record.SnapshotAt.In(s.location).Format(time.RFC3339), string(record.RankType), strconv.FormatInt(record.Rank, 10), record.Code, record.Name,
			strconv.FormatInt(record.LatestPriceRaw, 10), strconv.FormatFloat(record.ChangePct, 'f', 8, 64), strconv.FormatInt(record.DarkMoney, 10),
			strconv.FormatInt(record.RegularMoney, 10), strconv.FormatInt(record.MainMoneyInflow, 10), strconv.FormatFloat(record.DarkActivity, 'f', 8, 64),
			strconv.FormatFloat(record.DarkInflowRatio, 'f', 8, 64), strconv.FormatInt(record.UpCount, 10), strconv.FormatInt(record.DownCount, 10)})
	}
	writer.Flush()
}

func (s *Server) exportDailyClose(w http.ResponseWriter, r *http.Request) {
	tradeDate, ok := dateParam(w, r.URL.Query().Get("trade_date"))
	if !ok {
		return
	}
	records, err := s.store.DailyCloseRecords(r.Context(), tradeDate)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shadowflow-daily-close-%s.csv"`, tradeDate))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"trade_date", "snapshot_kind", "snapshot_at", "rank_type", "rank", "code", "name",
		"open_price", "high_price", "low_price", "close_price", "previous_close", "change_value", "change_pct",
		"volume", "turnover", "turnover_rate", "amplitude", "quote_available", "latest_price_raw",
		"dark_money", "regular_money", "main_money_inflow", "dark_activity", "dark_inflow_ratio", "up_count", "flat_count", "down_count"})
	for _, record := range records {
		_ = writer.Write([]string{record.TradeDate, string(graymarket.SnapshotDailyClose), record.SnapshotAt.In(s.location).Format(time.RFC3339), string(record.RankType), strconv.FormatInt(record.Rank, 10), record.Code, record.Name,
			strconv.FormatFloat(record.OpenPrice, 'f', 4, 64), strconv.FormatFloat(record.HighPrice, 'f', 4, 64), strconv.FormatFloat(record.LowPrice, 'f', 4, 64),
			strconv.FormatFloat(record.ClosePrice, 'f', 4, 64), strconv.FormatFloat(record.PreviousClose, 'f', 4, 64), strconv.FormatFloat(record.ChangeValue, 'f', 4, 64),
			strconv.FormatFloat(record.ChangePct, 'f', 8, 64), strconv.FormatInt(record.Volume, 10), strconv.FormatInt(record.Turnover, 10),
			strconv.FormatFloat(record.TurnoverRate, 'f', 8, 64), strconv.FormatFloat(record.Amplitude, 'f', 8, 64), strconv.FormatBool(record.QuoteAvailable),
			strconv.FormatInt(record.LatestPriceRaw, 10), strconv.FormatInt(record.DarkMoney, 10),
			strconv.FormatInt(record.RegularMoney, 10), strconv.FormatInt(record.MainMoneyInflow, 10), strconv.FormatFloat(record.DarkActivity, 'f', 8, 64),
			strconv.FormatFloat(record.DarkInflowRatio, 'f', 8, 64), strconv.FormatInt(record.UpCount, 10), strconv.FormatInt(record.FlatCount, 10), strconv.FormatInt(record.DownCount, 10)})
	}
	writer.Flush()
}

func boardTypeParam(w http.ResponseWriter, value string) (graymarket.RankType, bool) {
	rankType, err := graymarket.ParseRankType(value)
	if err != nil || rankType == graymarket.RankStock {
		writeError(w, http.StatusBadRequest, "invalid_type", "type must be industry or concept")
		return "", false
	}
	return rankType, true
}

func relationBoardTypeParam(w http.ResponseWriter, value string) (graymarket.BoardType, bool) {
	boardType, err := graymarket.ParseBoardType(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_type", "type must be industry or concept")
		return "", false
	}
	return boardType, true
}

func optionalAsOf(w http.ResponseWriter, r *http.Request, location *time.Location) (string, bool) {
	value := r.URL.Query().Get("as_of")
	if value == "" {
		return time.Now().In(location).Format("2006-01-02"), true
	}
	return dateParam(w, value)
}

func stockCodeValid(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func dateParam(w http.ResponseWriter, value string) (string, bool) {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_date", "date must use YYYY-MM-DD")
		return "", false
	}
	return value, true
}

func rangeParams(w http.ResponseWriter, r *http.Request, location *time.Location) (time.Time, time.Time, bool) {
	fromRaw, toRaw := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fromRaw == "" || toRaw == "" {
		writeError(w, http.StatusBadRequest, "invalid_range", "from and to are required")
		return time.Time{}, time.Time{}, false
	}
	from, err := parseDateOrTime(fromRaw, false, location)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_from", err.Error())
		return time.Time{}, time.Time{}, false
	}
	to, err := parseDateOrTime(toRaw, true, location)
	if err != nil || to.Before(from) {
		writeError(w, http.StatusBadRequest, "invalid_to", "to must be a valid date/time after from")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseDateOrTime(value string, end bool, location *time.Location) (time.Time, error) {
	if len(value) == 10 {
		parsed, err := time.ParseInLocation("2006-01-02", value, location)
		if err != nil {
			return time.Time{}, err
		}
		if end {
			parsed = parsed.Add(24*time.Hour - time.Nanosecond)
		}
		return parsed, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.New("must use YYYY-MM-DD or RFC3339")
	}
	return parsed, nil
}

func marketStatus(value time.Time) string {
	day := value.Weekday()
	if day == time.Saturday || day == time.Sunday {
		return "closed"
	}
	clock := value.Format("15:04")
	switch {
	case clock < "09:30":
		return "pre_open"
	case clock <= "11:30", clock >= "13:00" && clock <= "15:00":
		return "open"
	case clock < "13:00":
		return "lunch_break"
	default:
		return "closed"
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Error("api request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "request failed")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, envelope{Error: &apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ = strings.Builder{}
