package tradingcalendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

type Calendar struct {
	holiday map[string]struct{}
	workday map[string]struct{}
}

type fileData struct {
	Holidays []string `json:"holidays"`
	Workdays []string `json:"workdays"`
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
	for _, value := range data.Holidays {
		if err := validateDate(value); err != nil {
			return nil, fmt.Errorf("invalid holiday %q: %w", value, err)
		}
		calendar.holiday[value] = struct{}{}
	}
	for _, value := range data.Workdays {
		if err := validateDate(value); err != nil {
			return nil, fmt.Errorf("invalid workday %q: %w", value, err)
		}
		if _, duplicate := calendar.holiday[value]; duplicate {
			return nil, fmt.Errorf("date %s is listed as both holiday and workday", value)
		}
		calendar.workday[value] = struct{}{}
	}
	return calendar, nil
}

func validateDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return errors.New("date must use YYYY-MM-DD")
	}
	return nil
}

func (c *Calendar) IsTradingDay(date time.Time) bool {
	key := date.Format("2006-01-02")
	if _, ok := c.workday[key]; ok {
		return true
	}
	if _, ok := c.holiday[key]; ok {
		return false
	}
	return date.Weekday() >= time.Monday && date.Weekday() <= time.Friday
}
