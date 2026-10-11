// Package publicnet models the public side of the host's network connection:
// its public IPv4 and IPv6 addresses and its connectivity to the internet.
//
// Each check is recorded as a probe. A [Snapshot] groups the probes of a
// single monitoring run and is persisted through a [SnapshotRepository].
package publicnet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"uuid"
)

// ErrInvalidSnapshot is returned by [NewSnapshot] when a probe is inconsistent.
// Every validation error wraps it.
var ErrInvalidSnapshot = errors.New("invalid public net snapshot")

// ProbeStatus is the outcome of a probe.
type ProbeStatus uint8

const (
	// StatusUnknown is the zero value. It marks a probe whose outcome was never
	// set and is never valid in a [Snapshot].
	StatusUnknown ProbeStatus = iota
	// StatusOK means the probe succeeded.
	StatusOK
	// StatusUnavailable means the probe could not succeed on this network,
	// for example an IPv6 lookup on a host without IPv6 connectivity. This is
	// expected and not an error.
	StatusUnavailable
	// StatusFailed means the probe should have succeeded but did not. The
	// probe's message describes what went wrong.
	StatusFailed
	// StatusDisabled means the probe was turned off in the configuration.
	StatusDisabled
)

// String returns the lowercase name of the status.
func (s ProbeStatus) String() string {
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

	return fmt.Sprintf("ProbeStatus(%d)", uint8(s))
}

// IsValid reports whether s is a known status other than [StatusUnknown].
func (s ProbeStatus) IsValid() bool {
	return s >= StatusOK && s <= StatusDisabled
}

// ConnMethod is the method a [ConnProbe] uses to reach its target.
type ConnMethod uint8

const (
	// MethodUnknown is the zero value and is never valid for an enabled probe.
	MethodUnknown ConnMethod = iota
	// MethodICMP sends ICMP echo requests (ping).
	MethodICMP
	// MethodTCP opens TCP connections to the target.
	MethodTCP
	// MethodHTTP sends HTTP requests to the target.
	MethodHTTP
)

// String returns the lowercase name of the method.
func (m ConnMethod) String() string {
	switch m {
	case MethodUnknown:
		return "unknown"
	case MethodICMP:
		return "icmp"
	case MethodTCP:
		return "tcp"
	case MethodHTTP:
		return "http"
	}

	return fmt.Sprintf("ConnMethod(%d)", uint8(m))
}

// IsValid reports whether m is a known method other than [MethodUnknown].
func (m ConnMethod) IsValid() bool {
	return m >= MethodICMP && m <= MethodHTTP
}

// ProbeResult holds the outcome and timing that every probe has in common.
type ProbeResult struct {
	// Status is the outcome of the probe.
	Status ProbeStatus
	// Message describes the outcome, typically the error text of a failed or
	// unavailable probe. It is empty when there is nothing to report.
	Message string
	// StartedAt is when the probe started.
	StartedAt time.Time
	// Duration is how long the probe took.
	Duration time.Duration
}

// IPAddrProbe is the result of looking up the host's public IP address of one
// family.
type IPAddrProbe struct {
	ProbeResult

	// Source identifies the lookup service, for example its URL.
	Source string
	// Addr is the public address. It is set only when the status is
	// [StatusOK].
	Addr netip.Addr
}

// ConnProbe is the result of checking connectivity to a target server over
// one IP family.
type ConnProbe struct {
	ProbeResult

	// Method is how the target was contacted.
	Method ConnMethod
	// Target is the address of the server that was contacted.
	Target netip.Addr
	// Ping holds the attempt counts and round-trip times.
	Ping PingStats
}

// Connected reports whether the target was reached.
func (p ConnProbe) Connected() bool {
	return p.Status == StatusOK
}

// PingStats summarizes the attempts a [ConnProbe] made. For [MethodICMP] an
// attempt is an echo request; for the other methods it is a connection or
// request.
type PingStats struct {
	PacketsSent     int
	PacketsReceived int
	RoundTripMin    time.Duration
	RoundTripMax    time.Duration
	RoundTripAvg    time.Duration
	Jitter          time.Duration
}

// PacketsLost returns the number of attempts that got no response.
func (s PingStats) PacketsLost() int {
	return s.PacketsSent - s.PacketsReceived
}

// Snapshot is the state of the host's public network at one point in time.
type Snapshot struct {
	ID        uuid.UUID
	IPv4      IPAddrProbe
	IPv6      IPAddrProbe
	ConnIPv4  ConnProbe
	ConnIPv6  ConnProbe
	Timestamp time.Time
}

// NewSnapshot validates the probes and returns a snapshot with a new
// time-ordered ID and the timestamp converted to UTC.
//
// IPv4-mapped IPv6 addresses are converted to plain IPv4 addresses before they
// are checked. All validation errors are joined and wrap [ErrInvalidSnapshot].
func NewSnapshot(timestamp time.Time, ipv4, ipv6 IPAddrProbe, connIPv4, connIPv6 ConnProbe) (*Snapshot, error) {
	if timestamp.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is zero", ErrInvalidSnapshot)
	}

	ipv4, errIPv4 := ipv4.normalized("ipv4", netip.Addr.Is4)
	ipv6, errIPv6 := ipv6.normalized("ipv6", netip.Addr.Is6)
	connIPv4, errConnIPv4 := connIPv4.normalized("conn ipv4", netip.Addr.Is4)
	connIPv6, errConnIPv6 := connIPv6.normalized("conn ipv6", netip.Addr.Is6)

	err := errors.Join(errIPv4, errIPv6, errConnIPv4, errConnIPv6)
	if err != nil {
		return nil, err
	}

	return &Snapshot{
		ID:        uuid.NewV7(),
		IPv4:      ipv4,
		IPv6:      ipv6,
		ConnIPv4:  connIPv4,
		ConnIPv6:  connIPv6,
		Timestamp: timestamp.UTC(),
	}, nil
}

func (r ProbeResult) validate(probe string) error {
	if !r.Status.IsValid() {
		return fmt.Errorf("%w: %s: invalid status %s", ErrInvalidSnapshot, probe, r.Status)
	}

	return nil
}

func (p IPAddrProbe) normalized(probe string, inFamily func(netip.Addr) bool) (IPAddrProbe, error) {
	err := p.validate(probe)
	if err != nil {
		return IPAddrProbe{}, err
	}

	p.Addr = p.Addr.Unmap()

	switch {
	case p.Status == StatusOK && !p.Addr.IsValid():
		return IPAddrProbe{}, fmt.Errorf("%w: %s: missing address", ErrInvalidSnapshot, probe)
	case p.Status == StatusOK && !inFamily(p.Addr):
		return IPAddrProbe{}, fmt.Errorf("%w: %s: address %s has wrong family", ErrInvalidSnapshot, probe, p.Addr)
	case p.Status != StatusOK && p.Addr.IsValid():
		return IPAddrProbe{}, fmt.Errorf(
			"%w: %s: address %s set on probe with status %s",
			ErrInvalidSnapshot, probe, p.Addr, p.Status,
		)
	}

	return p, nil
}

func (p ConnProbe) normalized(probe string, inFamily func(netip.Addr) bool) (ConnProbe, error) {
	err := p.validate(probe)
	if err != nil {
		return ConnProbe{}, err
	}

	if p.Status == StatusDisabled {
		return p, nil
	}

	p.Target = p.Target.Unmap()

	switch {
	case !p.Method.IsValid():
		return ConnProbe{}, fmt.Errorf("%w: %s: invalid method %s", ErrInvalidSnapshot, probe, p.Method)
	case !p.Target.IsValid():
		return ConnProbe{}, fmt.Errorf("%w: %s: missing target", ErrInvalidSnapshot, probe)
	case !inFamily(p.Target):
		return ConnProbe{}, fmt.Errorf("%w: %s: target %s has wrong family", ErrInvalidSnapshot, probe, p.Target)
	case p.Ping.PacketsSent < 0 || p.Ping.PacketsReceived < 0 || p.Ping.PacketsReceived > p.Ping.PacketsSent:
		return ConnProbe{}, fmt.Errorf(
			"%w: %s: invalid packet counts (sent %d, received %d)",
			ErrInvalidSnapshot, probe, p.Ping.PacketsSent, p.Ping.PacketsReceived,
		)
	case p.Status == StatusOK && p.Ping.PacketsReceived == 0:
		return ConnProbe{}, fmt.Errorf("%w: %s: status ok but no packets received", ErrInvalidSnapshot, probe)
	}

	return p, nil
}

// SnapshotRepository persists snapshots.
type SnapshotRepository interface {
	SavePublicNetSnapshot(ctx context.Context, snap *Snapshot) error
}
