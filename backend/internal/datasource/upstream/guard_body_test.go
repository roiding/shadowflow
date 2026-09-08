package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type bodyTestTransport func(*http.Request) (*http.Response, error)

func (f bodyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type bodyReadError struct{ err error }

func (r bodyReadError) Read([]byte) (int, error) { return 0, r.err }

func bodyTestGuard(body func() io.ReadCloser) *Guard {
	return New(&http.Client{Transport: bodyTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body(), ContentLength: -1}, nil
	})}, Options{MaxConcurrency: 2, RatePerSecond: 100000, FailureThreshold: 3, OpenDuration: time.Minute, RecoverySuccesses: 2})
}

func bodyTestRequest(t *testing.T, g *Guard, ctx context.Context) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := g.Do(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestGuardTripsOnConsecutiveTruncatedBodies(t *testing.T) {
	g := bodyTestGuard(func() io.ReadCloser {
		return io.NopCloser(io.MultiReader(strings.NewReader("partial"), bodyReadError{io.ErrUnexpectedEOF}))
	})
	for i := range 3 {
		response := bodyTestRequest(t, g, context.Background())
		if _, err := io.ReadAll(response.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("body error=%v", err)
		}
		// Repeated errors and Close must not report more than one outcome.
		_, _ = response.Body.Read(make([]byte, 1))
		_ = response.Body.Close()
		if i < 2 && g.State() != StateClosed {
			t.Fatal("one body counted as multiple failures")
		}
	}
	r, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if _, err := g.Do(context.Background(), r); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("truncated bodies did not open circuit: %v", err)
	}
	if len(g.semaphore) != 0 {
		t.Fatal("body failure leaked concurrency slots")
	}
}

func TestGuardOnlyCompleteBodyResetsFailures(t *testing.T) {
	g := bodyTestGuard(func() io.ReadCloser { return io.NopCloser(strings.NewReader("ok")) })
	g.record("example.test", false)
	response := bodyTestRequest(t, g, context.Background())
	_ = response.Body.Close()
	if len(g.circuits["example.test"].failures) != 1 {
		t.Fatal("headers or abandoned body reset failure count")
	}
	response = bodyTestRequest(t, g, context.Background())
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if len(g.circuits["example.test"].failures) != 0 || len(g.semaphore) != 0 {
		t.Fatal("complete response did not reset failures and release slot")
	}
}

func TestGuardHalfOpenProbeHeldUntilBodyOutcome(t *testing.T) {
	for _, outcome := range []string{"complete", "truncated", "abandoned", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			g := bodyTestGuard(func() io.ReadCloser {
				if outcome == "truncated" || outcome == "canceled" {
					return io.NopCloser(bodyReadError{io.ErrUnexpectedEOF})
				}
				return io.NopCloser(strings.NewReader("ok"))
			})
			g.circuits["example.test"] = &circuitState{openUntil: time.Now().Add(-time.Second)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response := bodyTestRequest(t, g, ctx)
			r, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
			if _, err := g.Do(context.Background(), r); !errors.Is(err, ErrCircuitOpen) {
				t.Fatalf("second probe allowed while first body is unread: %v", err)
			}
			if outcome == "canceled" {
				cancel()
			}
			if outcome != "abandoned" {
				_, _ = io.ReadAll(response.Body)
			}
			_ = response.Body.Close()
			circuit := g.circuits["example.test"]
			if circuit.halfOpenProbe || len(g.semaphore) != 0 {
				t.Fatal("finished request leaked probe or slot")
			}
			if outcome == "truncated" {
				if g.State() != StateOpen {
					t.Fatal("failed probe did not reopen circuit")
				}
				return
			}
			wantSuccesses := 0
			if outcome == "complete" {
				wantSuccesses = 1
			}
			if g.State() != StateHalfOpen || circuit.halfOpenSuccesses != wantSuccesses {
				t.Fatalf("outcome counted incorrectly: state=%s successes=%d", g.State(), circuit.halfOpenSuccesses)
			}
		})
	}
}

func TestGuardCallerCancellationIsNeutralButTransportTimeoutFails(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		for _, canceled := range []bool{false, true} {
			t.Run(stage+map[bool]string{true: " caller canceled", false: " transport timeout"}[canceled], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				g := New(&http.Client{Transport: bodyTestTransport(func(*http.Request) (*http.Response, error) {
					if stage == "headers" {
						if canceled {
							cancel()
						}
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bodyReadError{context.DeadlineExceeded})}, nil
				})}, Options{FailureThreshold: 1})
				r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
				response, err := g.Do(ctx, r)
				if stage == "body" {
					if err != nil {
						t.Fatal(err)
					}
					if canceled {
						cancel()
					}
					_, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout error=%v", err)
				}
				want := StateOpen
				if canceled {
					want = StateClosed
				}
				if g.State() != want || len(g.semaphore) != 0 {
					t.Fatalf("state=%s want=%s slots=%d", g.State(), want, len(g.semaphore))
				}
			})
		}
	}
}
