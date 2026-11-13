package marketdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

var ErrProvidersUnavailable = errors.New("market data providers are cooling down")

// Router owns provider selection for one market-data capability. Providers are
// kept in priority order; adding a supplier does not change callers.
type Router[T any] struct {
	name     string
	cooldown time.Duration
	logger   *slog.Logger
	clock    func() time.Time
	mu       sync.Mutex
	entries  []providerEntry[T]
}

type providerEntry[T any] struct {
	name   string
	source T
	down   time.Time
}

type RouterOptions struct {
	Cooldown time.Duration
	Logger   *slog.Logger
	Clock    func() time.Time
}

type NamedProvider[T any] struct {
	Name   string
	Source T
}

func NewRouter[T any](name string, providers []NamedProvider[T], options RouterOptions) (*Router[T], error) {
	if len(providers) == 0 {
		return nil, errors.New("market data router requires at least one provider")
	}
	if options.Cooldown <= 0 {
		options.Cooldown = time.Minute
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	entries := make([]providerEntry[T], 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if provider.Name == "" {
			return nil, errors.New("market data provider name is required")
		}
		if _, duplicate := seen[provider.Name]; duplicate {
			return nil, fmt.Errorf("duplicate market data provider %s", provider.Name)
		}
		seen[provider.Name] = struct{}{}
		entries = append(entries, providerEntry[T]{name: provider.Name, source: provider.Source})
	}
	return &Router[T]{
		name: name, cooldown: options.Cooldown, logger: options.Logger, clock: options.Clock, entries: entries,
	}, nil
}

// Do tries the healthy providers in configured order. A failed provider is
// skipped for every subsequent call until its cooldown expires.
func (r *Router[T]) Do(ctx context.Context, operation func(context.Context, T) error) error {
	entries := r.healthy()
	if len(entries) == 0 {
		return fmt.Errorf("%s: %w", r.name, ErrProvidersUnavailable)
	}
	var combined error
	for _, entry := range entries {
		err := operation(ctx, entry.source)
		if err == nil {
			r.recover(entry.name)
			return nil
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		combined = errors.Join(combined, fmt.Errorf("%s: %w", entry.name, err))
		if !errors.Is(err, graymarket.ErrInstrumentUnavailable) {
			r.fail(entry.name, err)
		}
	}
	return combined
}

func (r *Router[T]) healthy() []providerEntry[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	result := make([]providerEntry[T], 0, len(r.entries))
	for _, entry := range r.entries {
		if entry.down.IsZero() || !now.Before(entry.down) {
			result = append(result, entry)
		}
	}
	return result
}

func (r *Router[T]) fail(name string, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	until := r.clock().Add(r.cooldown)
	for index := range r.entries {
		if r.entries[index].name != name {
			continue
		}
		if r.entries[index].down.IsZero() || until.After(r.entries[index].down) {
			r.entries[index].down = until
		}
		break
	}
	if r.logger != nil {
		r.logger.Warn("market data provider cooling down", "capability", r.name, "provider", name, "until", until.UTC(), "error", cause)
	}
}

func (r *Router[T]) recover(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.entries {
		if r.entries[index].name == name {
			r.entries[index].down = time.Time{}
			return
		}
	}
}
