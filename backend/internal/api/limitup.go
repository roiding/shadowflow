package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
)

type boardThemeFallbackReader interface {
	StockBoardRelationsBatch(context.Context, []string, string) ([]graymarket.StockBoardRelation, error)
	CurrentBoardMembershipStats(context.Context) (map[string]int, int, error)
}

type candidateHistoryReader interface {
	ReadCandidateHistoryMetrics(context.Context, string, []string, int, int) (map[string]limitup.CandidateHistoryMetrics, error)
}

// Quotes are fetched through a batched upstream endpoint. A larger batch
// avoids one HTTP round trip per handful of candidates. Dark-money ticks are
// deliberately not part of this loop: they require one request per stock and
// are read once, only for the small pool that can still enter the top N.
const todayCandidateBatchSize = 100

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
	if themeReader, ok := server.store.(limitup.ThemeReader); ok {
		themeAnalysis, themeErr := loadThemeAnalysis(request.Context(), themeReader, query, code, result.Links)
		if themeErr != nil {
			server.internalError(response, themeErr)
			return
		}
		if themeAnalysis.Profile.Code != "" {
			result.Theme = &themeAnalysis
		}
	}
	writeJSON(response, http.StatusOK, envelope{Data: result})
}

func loadThemeAnalysis(
	ctx context.Context,
	reader limitup.ThemeReader,
	query limitup.Query,
	code string,
	rawLinks []limitup.Link,
) (limitup.ThemeAnalysis, error) {
	normalized, from, err := limitup.NormalizeQuery(query)
	if err != nil {
		return limitup.ThemeAnalysis{}, err
	}
	profiles, err := reader.ReadThemeProfiles(ctx, from, normalized.AsOf, normalized.Scope, nil)
	if err != nil {
		return limitup.ThemeAnalysis{}, err
	}
	byCode := make(map[string]limitup.ThemeStockProfile, len(profiles))
	for _, profile := range profiles {
		byCode[profile.Code] = profile
	}
	rows, err := reader.ReadThemeCooccurrences(ctx, from, normalized.AsOf, normalized.Scope, code)
	if err != nil {
		return limitup.ThemeAnalysis{}, err
	}
	return limitup.BuildThemeAnalysis(code, byCode, rows, rawLinks), nil
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
	var snapshot limitup.IntradaySnapshot
	var err error
	if reader, ok := server.limitUpIntraday.(limitup.IntradayPoolsReader); ok {
		snapshot, err = reader.FetchIntradayToday(request.Context(), scope)
	} else {
		snapshot, err = server.limitUpIntraday.FetchSealedToday(request.Context(), scope)
	}
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
	themeProfiles := make(map[string]limitup.ThemeStockProfile)
	var boardReader boardThemeFallbackReader
	var boardCounts map[string]int
	var boardDocuments int
	var boardStatsLoaded bool
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
		if themeReader, ok := server.store.(limitup.ThemeReader); ok {
			profiles, themeErr := themeReader.ReadThemeProfiles(request.Context(), historyFrom, historyAsOf, scope, nil)
			if themeErr != nil {
				server.internalError(response, themeErr)
				return
			}
			for _, profile := range profiles {
				themeProfiles[profile.Code] = profile
			}
		}
	}
	if reader, ok := server.store.(boardThemeFallbackReader); ok {
		boardReader = reader
	}
	loadBoardFallbacks := func(codes []string) bool {
		if boardReader == nil || len(codes) == 0 {
			return false
		}
		if !boardStatsLoaded {
			counts, documents, boardErr := boardReader.CurrentBoardMembershipStats(request.Context())
			if boardErr != nil {
				server.logger.Warn("read Eastmoney board fallback statistics", "error", boardErr)
				boardReader = nil
				return false
			}
			boardCounts, boardDocuments, boardStatsLoaded = counts, documents, true
		}
		relations, boardErr := boardReader.StockBoardRelationsBatch(request.Context(), codes, snapshot.TradeDate)
		if boardErr != nil {
			server.logger.Warn("read Eastmoney board fallback relations", "error", boardErr)
			return false
		}
		memberships := make([]limitup.BoardMembership, 0, len(relations))
		for _, relation := range relations {
			memberships = append(memberships, limitup.BoardMembership{
				StockCode: relation.StockCode, StockName: relation.StockName,
				BoardCode: relation.BoardCode, BoardName: relation.BoardName,
				BoardType: string(relation.BoardType), SourceOrder: relation.SourceOrder,
			})
		}
		before := len(themeProfiles)
		themeProfiles = limitup.AddBoardThemeFallbacks(
			themeProfiles, codes, memberships, boardCounts, boardDocuments,
		)
		return len(themeProfiles) > before
	}
	memberCodes := make([]string, 0, len(snapshot.Events))
	for _, event := range snapshot.Events {
		memberCodes = append(memberCodes, event.Code)
	}
	loadBoardFallbacks(memberCodes)
	input := limitup.TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: historyFrom,
		LookbackDays: days, MinOverlap: minOverlap, TopCores: top, CandidateLimit: candidates,
		IncludeAllCandidates: true,
		ThemeProfiles:        themeProfiles,
	}
	result, err := limitup.AnalyzeToday(request.Context(), input)
	if err != nil {
		server.limitUpError(response, err)
		return
	}
	if loadBoardFallbacks(todayCandidateQuoteCodes(result)) {
		input.ThemeProfiles = themeProfiles
		result, err = limitup.AnalyzeToday(request.Context(), input)
		if err != nil {
			server.limitUpError(response, err)
			return
		}
	}
	liveQuoteStatus := "disabled"
	liveDarkMoneyStatus := "disabled"
	rankingStats := todayRankingStats{}
	if server.limitUpQuotes != nil {
		if len(result.Candidates) == 0 {
			liveQuoteStatus = "empty"
			liveDarkMoneyStatus = "empty"
		} else {
			result, rankingStats, err = server.rankTodayCandidates(request.Context(), input, result, historyAsOf, candidates)
			if err != nil {
				server.logger.Warn("rank today-core candidates", "error", err)
				writeError(response, http.StatusServiceUnavailable, "candidate_data_unavailable", "盘中候选数据不完整，无法保证排序；请稍后重试")
				return
			}
			liveQuoteStatus = "ready"
			if result.LiveUnavailable {
				liveQuoteStatus = "partial"
			}
			liveDarkMoneyStatus = rankingStats.DarkStatus
		}
	}
	if !result.LiveCandidate && len(result.Candidates) > candidates {
		result.Candidates = result.Candidates[:candidates]
	}
	result.CandidateLimit = candidates
	writeJSON(response, http.StatusOK, envelope{Data: result, Meta: map[string]any{
		"snapshot_only": true, "history_excludes_today": true,
		"snapshot_fetched_at": snapshot.FetchedAt, "live_candidates": result.LiveCandidate,
		"live_quote_status": liveQuoteStatus, "live_dark_money_status": liveDarkMoneyStatus,
		"candidate_history_days":     limitup.TodayLiveHistoryWindowDays,
		"candidate_static_count":     rankingStats.StaticCount,
		"candidate_quote_read_count": rankingStats.QuoteReadCount,
		"candidate_coarse_batches":   rankingStats.CoarseBatches,
		"candidate_analyze_calls":    rankingStats.AnalyzeCalls,
		"dark_money_pool_count":      rankingStats.DarkPoolCount,
		"dark_money_requested_count": rankingStats.DarkRequestedCount,
		"dark_money_available_count": rankingStats.DarkAvailableCount,
	}})
}

func todayCandidateQuoteCodes(result limitup.TodayNetwork) []string {
	seen := make(map[string]struct{}, len(result.Candidates))
	codes := make([]string, 0, len(result.Candidates))
	add := func(code string) {
		if code == "" {
			return
		}
		if _, duplicate := seen[code]; duplicate {
			return
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	for _, candidate := range result.Candidates {
		add(candidate.Code)
	}
	for _, group := range result.MainlineCandidates {
		for _, candidate := range group.Candidates {
			add(candidate.Code)
		}
	}
	sort.Strings(codes)
	return codes
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
