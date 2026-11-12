package limitup

// RescoreTodayCandidates reuses the static candidate graph and recomputes only
// the live, dark-money and risk components. The expensive historical graph is
// built once by AnalyzeToday; the intraday ranking loop must not rebuild it for
// every quote batch.
//
// When includeAll is true the returned Candidates keep every selected code so
// the caller can prove the Nth-score boundary. When false the list is limited
// to candidateLimit, matching the public response.
func RescoreTodayCandidates(
	base TodayNetwork,
	live map[string]LiveQuote,
	history map[string]CandidateHistoryMetrics,
	darkMoney map[string]LiveDarkMoney,
	filter map[string]struct{},
	candidateLimit int,
	includeAll bool,
) TodayNetwork {
	maxHistory := 0.0
	candidates := make([]TodayCandidate, 0, len(base.Candidates))
	for _, candidate := range base.Candidates {
		maxHistory = max(maxHistory, candidate.HistoryScore)
		if filter != nil {
			if _, included := filter[candidate.Code]; !included {
				continue
			}
		}
		candidates = append(candidates, candidate)
	}
	candidates = scoreTodayCandidates(candidates, live, history, darkMoney, maxHistory)
	mainlineCandidates := buildMainlineCandidateGroups(candidates, base.ThemeMainline, candidateLimit, live != nil)
	if !includeAll && candidateLimit > 0 && len(candidates) > candidateLimit {
		candidates = candidates[:candidateLimit]
	}
	liveCandidate := live != nil
	liveUnavailable := false
	if liveCandidate {
		for _, candidate := range candidates {
			if !candidate.LiveAvailable {
				liveUnavailable = true
				break
			}
		}
		if !liveUnavailable {
			for _, group := range mainlineCandidates {
				for _, candidate := range group.Candidates {
					if !candidate.LiveAvailable {
						liveUnavailable = true
						break
					}
				}
				if liveUnavailable {
					break
				}
			}
		}
	}
	base.Candidates = candidates
	base.MainlineCandidates = mainlineCandidates
	base.CandidateLimit = candidateLimit
	base.LiveCandidate = liveCandidate
	base.LiveUnavailable = liveUnavailable
	return base
}
