package limitup

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type Service struct {
	reader Reader
}

func New(reader Reader) *Service {
	return &Service{reader: reader}
}

func (service *Service) load(ctx context.Context, query Query) (Query, Coverage, Dataset, error) {
	query, fromDate, err := NormalizeQuery(query)
	if err != nil {
		return Query{}, Coverage{}, Dataset{}, err
	}
	dataset, err := service.reader.ReadLimitUpWindow(ctx, fromDate, query.AsOf, query.Scope)
	if err != nil {
		return Query{}, Coverage{}, Dataset{}, err
	}
	if err := validateDataset(ctx, dataset, fromDate, query.AsOf); err != nil {
		return Query{}, Coverage{}, Dataset{}, err
	}
	sort.Slice(dataset.Days, func(left, right int) bool { return dataset.Days[left].TradeDate < dataset.Days[right].TradeDate })
	coverage := Coverage{
		Source: SourceTHS, Scope: query.Scope, FromDate: fromDate, AsOf: query.AsOf, LookbackDays: query.Days,
		ObservedDays: len(dataset.Days), MinimumSamples: MinimumSamples, SampleSufficient: len(dataset.Days) >= MinimumSamples,
	}
	if len(dataset.Days) > 0 {
		coverage.FirstDate = dataset.Days[0].TradeDate
		coverage.LatestDate = dataset.Days[len(dataset.Days)-1].TradeDate
	}
	return query, coverage, dataset, nil
}

func validateDataset(ctx context.Context, dataset Dataset, fromDate, asOf string) error {
	if len(dataset.Events) > MaxWindowEvents {
		return fmt.Errorf("%w: too many event records; reduce the date range", ErrInvalid)
	}
	dayCounts := make(map[string]Day, len(dataset.Days))
	for _, day := range dataset.Days {
		if _, duplicate := dayCounts[day.TradeDate]; duplicate || day.TradeDate < fromDate || day.TradeDate > asOf {
			return fmt.Errorf("inconsistent limit-up date window")
		}
		dayCounts[day.TradeDate] = day
	}
	seen := make(map[string]map[string]bool, len(dataset.Events))
	for _, event := range dataset.Events {
		if err := ctx.Err(); err != nil {
			return err
		}
		day, exists := dayCounts[event.TradeDate]
		if !exists {
			return fmt.Errorf("limit-up event has no complete day")
		}
		if err := validateEvent(event, event.TradeDate); err != nil {
			return fmt.Errorf("invalid stored limit-up event: %v", err)
		}
		key := event.TradeDate + ":" + event.Code
		if err := addPool(seen, key, event.Pool); err != nil {
			return fmt.Errorf("conflicting stored daily limit events: %v", err)
		}
		if event.Pool == PoolDown {
			day.DownCount--
		} else {
			day.TouchedCount--
		}
		if event.Pool == PoolSealed {
			day.SealedCount--
		} else if event.Pool == PoolBroken {
			day.BrokenCount--
		}
		dayCounts[event.TradeDate] = day
	}
	for _, day := range dayCounts {
		if day.SealedCount != 0 || day.BrokenCount != 0 || day.TouchedCount != 0 || day.DownCount != 0 {
			return fmt.Errorf("stored limit-up records do not match their complete day")
		}
	}
	return ctx.Err()
}

func (service *Service) Overview(ctx context.Context, query Query) (Overview, error) {
	_, coverage, dataset, err := service.load(ctx, query)
	if err != nil {
		return Overview{}, err
	}
	summaries := summarize(dataset.Events)
	stocks := make([]StockSummary, 0, len(summaries))
	for _, summary := range summaries {
		stocks = append(stocks, summary)
	}
	sort.Slice(stocks, func(left, right int) bool {
		if stocks[left].SealDays != stocks[right].SealDays {
			return stocks[left].SealDays > stocks[right].SealDays
		}
		if stocks[left].DownDays != stocks[right].DownDays {
			return stocks[left].DownDays > stocks[right].DownDays
		}
		if stocks[left].TouchDays != stocks[right].TouchDays {
			return stocks[left].TouchDays > stocks[right].TouchDays
		}
		return stocks[left].Code < stocks[right].Code
	})
	if dataset.Days == nil {
		dataset.Days = []Day{}
	}
	return Overview{Coverage: coverage, Days: dataset.Days, Stocks: stocks}, nil
}

func summarize(events []Event) map[string]StockSummary {
	summaries := make(map[string]StockSummary)
	for _, event := range events {
		summary := summaries[event.Code]
		if event.TradeDate >= summary.LastSeen {
			summary.Code, summary.Name, summary.MarketType = event.Code, event.Name, event.MarketType
			summary.LastSeen = event.TradeDate
		}
		if event.Pool != PoolDown {
			summary.TouchDays++
		}
		if event.Pool == PoolSealed {
			summary.SealDays++
		} else if event.Pool == PoolBroken {
			summary.BrokenDays++
		} else {
			summary.DownDays++
		}
		summaries[event.Code] = summary
	}
	return summaries
}

func (service *Service) Analyze(ctx context.Context, query Query, code string) (Analysis, error) {
	if !ValidCode(code) {
		return Analysis{}, fmt.Errorf("%w: stock code must contain six digits", ErrInvalid)
	}
	query, coverage, dataset, err := service.load(ctx, query)
	if err != nil {
		return Analysis{}, err
	}
	summaries := summarize(dataset.Events)
	summary, exists := summaries[code]
	if !exists {
		return Analysis{}, ErrMissing
	}
	events := make([]Event, 0, summary.TouchDays+summary.DownDays)
	for _, event := range dataset.Events {
		if event.Code == code {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].TradeDate != events[right].TradeDate {
			return events[left].TradeDate > events[right].TradeDate
		}
		return events[left].Pool < events[right].Pool
	})
	links, total, err := buildLinks(ctx, query, dataset, summaries, code)
	if err != nil {
		return Analysis{}, err
	}
	return Analysis{
		Coverage: coverage, Event: query.Event, MinOverlap: query.MinOverlap, TotalLinks: total,
		Profile: buildProfile(summary, events, coverage.ObservedDays), Links: links, Events: events,
	}, nil
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func medianClock(times []int) string {
	if len(times) == 0 {
		return ""
	}
	sort.Ints(times)
	middle := len(times) / 2
	median := times[middle]
	if len(times)%2 == 0 {
		median = (median + times[middle-1]) / 2
	}
	return time.Date(2000, 1, 1, 0, 0, 0, 0, Shanghai()).Add(time.Duration(median) * time.Second).Format("15:04:05")
}

func buildProfile(summary StockSummary, events []Event, observedDays int) Profile {
	profile := Profile{
		StockSummary: summary, SealRate: ratio(summary.SealDays, summary.TouchDays),
		BrokenRate: ratio(summary.BrokenDays, summary.TouchDays), EventFrequency: ratio(summary.TouchDays, observedDays),
		DownFrequency: ratio(summary.DownDays, observedDays),
		LimitTypes:    []CategoryCount{}, FirstTimeBuckets: []CategoryCount{},
	}
	openTotal := 0
	firstTimes := []int{}
	firstDownTimes := []int{}
	limitTypes := make(map[string]int)
	bucketLabels := []string{"09:30前", "09:30–10:00", "10:00–11:30", "13:00–14:00", "14:00后"}
	buckets := make([]int, len(bucketLabels))
	upDates, downDates := make(map[string]bool), make(map[string]bool)
	for _, event := range events {
		if event.Pool == PoolDown {
			downDates[event.TradeDate] = true
			if event.FirstDownAt != nil {
				downTime := event.FirstDownAt.In(Shanghai())
				firstDownTimes = append(firstDownTimes, downTime.Hour()*3600+downTime.Minute()*60+downTime.Second())
			}
			continue
		}
		upDates[event.TradeDate] = true
		opened := event.Pool == PoolBroken
		if event.OpenCount == nil {
			profile.OpenCountUnknownDays++
		} else {
			profile.OpenCountKnownDays++
			openTotal += *event.OpenCount
			opened = opened || *event.OpenCount > 0
			if *event.OpenCount > 0 && event.Pool == PoolSealed {
				profile.RecoveredDays++
			}
		}
		if opened {
			profile.OpenedDays++
		}
		if event.FirstLimitAt != nil {
			first := event.FirstLimitAt.In(Shanghai())
			seconds := first.Hour()*3600 + first.Minute()*60 + first.Second()
			firstTimes = append(firstTimes, seconds)
			switch {
			case seconds < 9*3600+30*60:
				buckets[0]++
			case seconds < 10*3600:
				buckets[1]++
			case seconds <= 11*3600+30*60:
				buckets[2]++
			case seconds < 14*3600:
				buckets[3]++
			default:
				buckets[4]++
			}
		}
		label := event.LimitType
		if label == "" {
			label = "未标注"
		}
		limitTypes[label]++
	}
	for date := range downDates {
		if upDates[date] {
			profile.UpToDownDays++
		}
	}
	profile.MeanOpenCount = ratio(openTotal, profile.OpenCountKnownDays)
	profile.RecoveryRate = ratio(profile.RecoveredDays, profile.OpenedDays)
	profile.FirstTimeSamples = len(firstTimes)
	profile.MedianFirstTime = medianClock(firstTimes)
	profile.FirstDownTimeSamples = len(firstDownTimes)
	profile.MedianFirstDownTime = medianClock(firstDownTimes)
	for position, label := range bucketLabels {
		profile.FirstTimeBuckets = append(profile.FirstTimeBuckets, CategoryCount{Label: label, Count: buckets[position]})
	}
	for label, count := range limitTypes {
		profile.LimitTypes = append(profile.LimitTypes, CategoryCount{Label: label, Count: count})
	}
	sort.Slice(profile.LimitTypes, func(left, right int) bool {
		if profile.LimitTypes[left].Count != profile.LimitTypes[right].Count {
			return profile.LimitTypes[left].Count > profile.LimitTypes[right].Count
		}
		return profile.LimitTypes[left].Label < profile.LimitTypes[right].Label
	})
	return profile
}

func buildLinks(ctx context.Context, query Query, dataset Dataset, summaries map[string]StockSummary, code string) ([]Link, int, error) {
	eventDays := make(map[string]int)
	byDate := make(map[string][]string, len(dataset.Days))
	anchorDates := make(map[string]struct{})
	for _, event := range dataset.Events {
		matches := query.Event == EventSealed && event.Pool == PoolSealed ||
			query.Event == EventTouched && event.Pool != PoolDown ||
			query.Event == EventDown && event.Pool == PoolDown
		if !matches {
			continue
		}
		eventDays[event.Code]++
		byDate[event.TradeDate] = append(byDate[event.TradeDate], event.Code)
		if event.Code == code {
			anchorDates[event.TradeDate] = struct{}{}
		}
	}
	candidates := make(map[string]Link)
	for _, day := range dataset.Days {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if _, matches := anchorDates[day.TradeDate]; !matches {
			continue
		}
		peers := byDate[day.TradeDate]
		for _, peer := range peers {
			if peer == code {
				continue
			}
			link := candidates[peer]
			link.Code = peer
			link.OverlapDays++
			link.HeatAdjustedOverlap += 1 / float64(max(1, len(peers)-1))
			link.SharedDates = append(link.SharedDates, day.TradeDate)
			candidates[peer] = link
		}
	}
	links := make([]Link, 0, len(candidates))
	for peer, link := range candidates {
		if link.OverlapDays < query.MinOverlap {
			continue
		}
		link.Name, link.MarketType = summaries[peer].Name, summaries[peer].MarketType
		link.AnchorDays, link.PeerDays = eventDays[code], eventDays[peer]
		anchor, neighbor, common, total := float64(link.AnchorDays), float64(link.PeerDays), float64(link.OverlapDays), float64(len(dataset.Days))
		link.Jaccard = common / (anchor + neighbor - common)
		link.ConditionalProbability, link.ReverseProbability = common/anchor, common/neighbor
		link.BaselineProbability = neighbor / total
		link.Lift = common * total / (anchor * neighbor)
		denominator := math.Sqrt(anchor * neighbor * (total - anchor) * (total - neighbor))
		if denominator > 0 {
			value := (common*total - anchor*neighbor) / denominator
			link.Phi = &value
		}
		link.SampleSufficient = len(dataset.Days) >= MinimumSamples && link.OverlapDays >= 3
		sort.Sort(sort.Reverse(sort.StringSlice(link.SharedDates)))
		links = append(links, link)
	}
	sort.Slice(links, func(left, right int) bool {
		if query.Order == "overlap" {
			if links[left].OverlapDays != links[right].OverlapDays {
				return links[left].OverlapDays > links[right].OverlapDays
			}
			if links[left].HeatAdjustedOverlap != links[right].HeatAdjustedOverlap {
				return links[left].HeatAdjustedOverlap > links[right].HeatAdjustedOverlap
			}
			return links[left].Code < links[right].Code
		}
		if links[left].HeatAdjustedOverlap != links[right].HeatAdjustedOverlap {
			return links[left].HeatAdjustedOverlap > links[right].HeatAdjustedOverlap
		}
		if links[left].OverlapDays != links[right].OverlapDays {
			return links[left].OverlapDays > links[right].OverlapDays
		}
		return links[left].Code < links[right].Code
	})
	totalLinks := len(links)
	if len(links) > query.Limit {
		links = links[:query.Limit]
	}
	return links, totalLinks, nil
}
