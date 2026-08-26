package tradingcalendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultSourceURL = "https://www.sse.com.cn/disclosure/dealinstruc/closed/"

var (
	tagPattern   = regexp.MustCompile(`<[^>]+>`)
	datePattern  = regexp.MustCompile(`([0-9]{1,2})月([0-9]{1,2})日`)
	spacePattern = regexp.MustCompile(`\s+`)
)

type Calendar struct {
	mu           sync.RWMutex
	holiday      map[string]struct{}
	workday      map[string]struct{}
	validThrough string
	updatedAt    string
	source       string
}

type fileData struct {
	Holidays     []string `json:"holidays"`
	Workdays     []string `json:"workdays"`
	ValidThrough string   `json:"valid_through,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	Source       string   `json:"source,omitempty"`
}

type Coverage struct {
	ValidThrough  string `json:"valid_through,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Source        string `json:"source,omitempty"`
	DaysRemaining int    `json:"days_remaining"`
	Expired       bool   `json:"expired"`
}

func Load(path string) (*Calendar, error) {
	calendar := &Calendar{holiday: make(map[string]struct{}), workday: make(map[string]struct{})}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return calendar, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trading calendar: %w", err)
	}
	var data fileData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parse trading calendar: %w", err)
	}
	if err := validateFileData(data); err != nil {
		return nil, err
	}
	calendar.replace(data)
	return calendar, nil
}

func validateFileData(data fileData) error {
	holidays := make(map[string]struct{}, len(data.Holidays))
	for _, value := range data.Holidays {
		if err := validateDate(value); err != nil {
			return fmt.Errorf("invalid holiday %q: %w", value, err)
		}
		holidays[value] = struct{}{}
	}
	for _, value := range data.Workdays {
		if err := validateDate(value); err != nil {
			return fmt.Errorf("invalid workday %q: %w", value, err)
		}
		if _, duplicate := holidays[value]; duplicate {
			return fmt.Errorf("date %s is listed as both holiday and workday", value)
		}
	}
	if data.ValidThrough != "" {
		if err := validateDate(data.ValidThrough); err != nil {
			return fmt.Errorf("invalid valid_through %q: %w", data.ValidThrough, err)
		}
	}
	if data.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, data.UpdatedAt); err != nil {
			return fmt.Errorf("invalid updated_at %q: %w", data.UpdatedAt, err)
		}
	}
	return nil
}

func validateDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return errors.New("date must use YYYY-MM-DD")
	}
	return nil
}

func (c *Calendar) replace(data fileData) {
	holiday := make(map[string]struct{}, len(data.Holidays))
	workday := make(map[string]struct{}, len(data.Workdays))
	for _, value := range data.Holidays {
		holiday[value] = struct{}{}
	}
	for _, value := range data.Workdays {
		workday[value] = struct{}{}
	}
	c.mu.Lock()
	c.holiday, c.workday = holiday, workday
	c.validThrough, c.updatedAt, c.source = data.ValidThrough, data.UpdatedAt, data.Source
	c.mu.Unlock()
}

func (c *Calendar) IsTradingDay(date time.Time) bool {
	key := date.Format("2006-01-02")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.workday[key]; ok {
		return true
	}
	if _, ok := c.holiday[key]; ok {
		return false
	}
	return date.Weekday() >= time.Monday && date.Weekday() <= time.Friday
}

func (c *Calendar) PreviousTradingDay(date time.Time) time.Time {
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location()).AddDate(0, 0, -1)
	// A corrupted or hand-edited calendar file could mark long stretches as
	// holidays; bound the walk so a bad file cannot spin this loop for years.
	for steps := 0; !c.IsTradingDay(day) && steps < 60; steps++ {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

func (c *Calendar) Coverage(now time.Time) Coverage {
	c.mu.RLock()
	validThrough, updatedAt, source := c.validThrough, c.updatedAt, c.source
	c.mu.RUnlock()
	result := Coverage{ValidThrough: validThrough, UpdatedAt: updatedAt, Source: source}
	if validThrough == "" {
		result.Expired = true
		return result
	}
	valid, err := time.ParseInLocation("2006-01-02", validThrough, now.Location())
	if err != nil {
		result.Expired = true
		return result
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	result.DaysRemaining = int(valid.Sub(today).Hours() / 24)
	result.Expired = result.DaysRemaining < 0
	return result
}

func (c *Calendar) RefreshIfNeeded(ctx context.Context, client *http.Client, path, sourceURL string, now time.Time, leadDays int) (bool, error) {
	if leadDays < 1 {
		leadDays = 45
	}
	coverage := c.Coverage(now)
	if coverage.ValidThrough != "" && coverage.DaysRemaining >= leadDays {
		return false, nil
	}
	// Derive the target year from the current coverage: once this year's
	// schedule is loaded, the only useful fetch is next year's. Deriving it
	// from the wall clock instead ("December means next year") made every
	// refresh from mid-November until December re-download the already-known
	// current year and rewrite the file daily without extending coverage.
	year := now.Year()
	if coverage.ValidThrough != "" {
		if valid, err := time.Parse("2006-01-02", coverage.ValidThrough); err == nil && valid.Year() >= year {
			year = valid.Year() + 1
		}
	}
	fetched, err := fetchAnnualSchedule(ctx, client, sourceURL, year, now)
	if err != nil {
		return false, err
	}
	data := c.mergedWith(fetched, year)
	if err := writeCalendar(path, data); err != nil {
		return false, err
	}
	c.replace(data)
	return true, nil
}

// mergedWith combines a freshly fetched one-year schedule with the entries
// already loaded for other years. Refreshing next year's schedule in December
// must not drop the current year's remaining holidays (e.g. the New Year
// closure starting Dec 31) or manually maintained workday adjustments.
func (c *Calendar) mergedWith(fetched fileData, year int) fileData {
	prefix := fmt.Sprintf("%04d-", year)
	newHolidays := make(map[string]struct{}, len(fetched.Holidays))
	for _, day := range fetched.Holidays {
		newHolidays[day] = struct{}{}
	}
	c.mu.RLock()
	holidays := make([]string, 0, len(c.holiday)+len(fetched.Holidays))
	for day := range c.holiday {
		if !strings.HasPrefix(day, prefix) {
			holidays = append(holidays, day)
		}
	}
	workdays := make([]string, 0, len(c.workday))
	for day := range c.workday {
		if _, clash := newHolidays[day]; !clash {
			workdays = append(workdays, day)
		}
	}
	validThrough := c.validThrough
	c.mu.RUnlock()
	holidays = append(holidays, fetched.Holidays...)
	sort.Strings(holidays)
	sort.Strings(workdays)
	if fetched.ValidThrough > validThrough {
		validThrough = fetched.ValidThrough
	}
	return fileData{Holidays: holidays, Workdays: workdays, ValidThrough: validThrough,
		UpdatedAt: fetched.UpdatedAt, Source: fetched.Source}
}

func fetchAnnualSchedule(ctx context.Context, client *http.Client, sourceURL string, year int, now time.Time) (fileData, error) {
	if sourceURL == "" {
		sourceURL = DefaultSourceURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fileData{}, err
	}
	request.Header.Set("Accept", "text/html")
	request.Header.Set("User-Agent", "shadowflow-calendar/1.0")
	response, err := client.Do(request)
	if err != nil {
		return fileData{}, fmt.Errorf("fetch exchange calendar: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fileData{}, fmt.Errorf("exchange calendar returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fileData{}, err
	}
	holidays, err := parseAnnualSchedule(string(body), year)
	if err != nil {
		return fileData{}, err
	}
	return fileData{
		Holidays: holidays, Workdays: []string{},
		ValidThrough: fmt.Sprintf("%04d-12-31", year),
		UpdatedAt:    now.UTC().Format(time.RFC3339), Source: sourceURL,
	}, nil
}

func parseAnnualSchedule(body string, year int) ([]string, error) {
	text := html.UnescapeString(tagPattern.ReplaceAllString(body, " "))
	text = spacePattern.ReplaceAllString(text, " ")
	startMarker := fmt.Sprintf("%d年休市安排", year)
	start := strings.Index(text, startMarker)
	if start < 0 {
		return nil, fmt.Errorf("exchange calendar does not contain %s", startMarker)
	}
	text = text[start+len(startMarker):]
	if end := strings.Index(text, "相关公告"); end >= 0 {
		text = text[:end]
	}
	holidays := make(map[string]struct{})
	for _, sentence := range strings.Split(text, "。") {
		if !strings.Contains(sentence, "休市") {
			continue
		}
		if index := strings.Index(sentence, "另外"); index >= 0 {
			sentence = sentence[:index]
		}
		matches := datePattern.FindAllStringSubmatch(sentence, -1)
		if len(matches) == 0 {
			continue
		}
		startDate, err := monthDay(year, matches[0])
		if err != nil {
			return nil, err
		}
		endDate := startDate
		if strings.Contains(sentence, "至") && len(matches) >= 2 {
			endDate, err = monthDay(year, matches[1])
			if err != nil {
				return nil, err
			}
		}
		if endDate.Before(startDate) || endDate.Sub(startDate) > 31*24*time.Hour {
			return nil, fmt.Errorf("invalid exchange holiday range %s to %s", startDate, endDate)
		}
		for day := startDate; !day.After(endDate); day = day.AddDate(0, 0, 1) {
			if day.Weekday() >= time.Monday && day.Weekday() <= time.Friday {
				holidays[day.Format("2006-01-02")] = struct{}{}
			}
		}
	}
	if len(holidays) < 7 {
		return nil, fmt.Errorf("exchange calendar for %d contains only %d weekday holidays", year, len(holidays))
	}
	result := make([]string, 0, len(holidays))
	for day := range holidays {
		result = append(result, day)
	}
	sort.Strings(result)
	return result, nil
}

func monthDay(year int, match []string) (time.Time, error) {
	var month, day int
	if _, err := fmt.Sscanf(match[1], "%d", &month); err != nil {
		return time.Time{}, err
	}
	if _, err := fmt.Sscanf(match[2], "%d", &day); err != nil {
		return time.Time{}, err
	}
	value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if int(value.Month()) != month || value.Day() != day {
		return time.Time{}, fmt.Errorf("invalid month/day %d/%d", month, day)
	}
	return value, nil
}

func writeCalendar(path string, data fileData) error {
	if err := validateFileData(data); err != nil {
		return err
	}
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("write temporary trading calendar: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("write temporary trading calendar: %w", err)
	}
	// fsync before rename: a crash between rename and writeback could
	// otherwise leave an empty calendar that fails Load on the next start.
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("sync temporary trading calendar: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("close temporary trading calendar: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace trading calendar: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
