// Package probe holds the building blocks shared by netward's snapshot
// domains: the outcome and timing of a single check and ping statistics.
package probe

import (
	"errors"
	"fmt"
	"time"
)

// Status is the outcome of a probe.
type Status uint8

const (
	// StatusUnknown is the zero value. It marks a probe whose outcome was never
	// set and is never valid in a snapshot.
	StatusUnknown Status = iota
	// StatusOK means the probe succeeded.
	StatusOK
	// StatusUnavailable means the probe could not succeed on this network,
	// for example an IPv6 lookup on a host without IPv6 connectivity. This is
	// expected and not an error.
	StatusUnavailable
	// StatusFailed means the probe should have succeeded but did not. The
	// result's message describes what went wrong.
	StatusFailed
	// StatusDisabled means the probe was turned off in the configuration.
	StatusDisabled
)

// String returns the lowercase name of the status.
func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusOK:
		return "ok"
	case StatusUnavailable:
		return "unavailable"
	case StatusFailed:
		return "failed"
	case StatusDisabled:
		return "disabled"
	}

	return fmt.Sprintf("Status(%d)", uint8(s))
}

// IsValid reports whether s is a known status other than [StatusUnknown].
func (s Status) IsValid() bool {
	return s >= StatusOK && s <= StatusDisabled
}

// Result holds the outcome and timing that every probe has in common.
type Result struct {
	// Status is the outcome of the probe.
	Status Status
	// Message describes the outcome, typically the error text of a failed or
	// unavailable probe. It is empty when there is nothing to report.
	Message string
	// StartedAt is when the probe started. It is zero for a probe that did
	// not run.
	StartedAt time.Time
	// Duration is how long the probe took.
	Duration time.Duration
}

// NewResult returns the result of a probe that started at startedAt, took
// duration and ended with err. A nil err means [StatusOK], an err matching
// one of unavailable means [StatusUnavailable] and any other err
// [StatusFailed]. The error text becomes the message.
func NewResult(startedAt time.Time, duration time.Duration, err error, unavailable ...error) Result {
	result := Result{
		Status:    StatusOK,
		Message:   "",
		StartedAt: startedAt.UTC(),
		Duration:  duration,
	}

	if err == nil {
		return result
	}

	result.Status = StatusFailed
	result.Message = err.Error()

	for _, target := range unavailable {
		if errors.Is(err, target) {
			result.Status = StatusUnavailable
		}
	}

	return result
}

// Disabled returns the result of a probe that was turned off.
func Disabled() Result {
	return Result{
		Status:    StatusDisabled,
		Message:   "",
		StartedAt: time.Time{},
		Duration:  0,
	}
}

// Validate reports whether the result has a valid status and a non-negative
// duration.
func (r Result) Validate() error {
	if !r.Status.IsValid() {
		return fmt.Errorf("invalid status %s", r.Status)
	}

	if r.Duration < 0 {
		return fmt.Errorf("negative duration %s", r.Duration)
	}

	return nil
}

// PingStats summarizes the echo requests of a reachability probe. For ICMP an
// attempt is an echo request; for other methods it is a connection or request.
type PingStats struct {
	// PacketsSent is the number of attempts made.
	PacketsSent int
	// PacketsReceived is the number of attempts that got a response.
	PacketsReceived int
	// RoundTripMin is the shortest round-trip time.
	RoundTripMin time.Duration
	// RoundTripMax is the longest round-trip time.
	RoundTripMax time.Duration
	// RoundTripAvg is the mean round-trip time.
	RoundTripAvg time.Duration
	// Jitter is the mean absolute difference between consecutive round-trip
	// times.
	Jitter time.Duration
}

// PacketsLost returns the number of attempts that got no response.
func (s PingStats) PacketsLost() int {
	return s.PacketsSent - s.PacketsReceived
}

// Validate reports whether the packet counts are consistent: neither is
// negative and no more packets were received than sent.
func (s PingStats) Validate() error {
	if s.PacketsSent < 0 || s.PacketsReceived < 0 || s.PacketsReceived > s.PacketsSent {
		return fmt.Errorf("invalid packet counts (sent %d, received %d)", s.PacketsSent, s.PacketsReceived)
	}

	return nil
}
