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

	"github.com/devusSs/netward/internal/domain/probe"
)

// ErrInvalidSnapshot is returned by [NewSnapshot] when a probe is inconsistent.
// Every validation error wraps it.
var ErrInvalidSnapshot = errors.New("invalid public net snapshot")

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

// IPAddrProbe is the result of looking up the host's public IP address of one
// family.
type IPAddrProbe struct {
	probe.Result

	// Source identifies the lookup service, for example its URL.
	Source string
	// Addr is the public address. It is set only when the status is
	// [probe.StatusOK].
	Addr netip.Addr
}

// ConnProbe is the result of checking connectivity to a target server over
// one IP family.
type ConnProbe struct {
	probe.Result

	// Method is how the target was contacted.
	Method ConnMethod
	// Target is the address of the server that was contacted.
	Target netip.Addr
	// Ping holds the attempt counts and round-trip times.
	Ping probe.PingStats
}

// Connected reports whether the target was reached.
func (p ConnProbe) Connected() bool {
	return p.Status == probe.StatusOK
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

func (p IPAddrProbe) normalized(name string, inFamily func(netip.Addr) bool) (IPAddrProbe, error) {
	err := p.Validate()
	if err != nil {
		return IPAddrProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, name, err)
	}

	p.Addr = p.Addr.Unmap()

	switch {
	case p.Status == probe.StatusOK && !p.Addr.IsValid():
		return IPAddrProbe{}, fmt.Errorf("%w: %s: missing address", ErrInvalidSnapshot, name)
	case p.Status == probe.StatusOK && !inFamily(p.Addr):
		return IPAddrProbe{}, fmt.Errorf("%w: %s: address %s has wrong family", ErrInvalidSnapshot, name, p.Addr)
	case p.Status != probe.StatusOK && p.Addr.IsValid():
		return IPAddrProbe{}, fmt.Errorf(
			"%w: %s: address %s set on probe with status %s",
			ErrInvalidSnapshot, name, p.Addr, p.Status,
		)
	}

	return p, nil
}

func (p ConnProbe) normalized(name string, inFamily func(netip.Addr) bool) (ConnProbe, error) {
	err := p.Validate()
	if err != nil {
		return ConnProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, name, err)
	}

	if p.Status == probe.StatusDisabled {
		return p, nil
	}

	p.Target = p.Target.Unmap()

	err = p.Ping.Validate()

	switch {
	case !p.Method.IsValid():
		return ConnProbe{}, fmt.Errorf("%w: %s: invalid method %s", ErrInvalidSnapshot, name, p.Method)
	case !p.Target.IsValid():
		return ConnProbe{}, fmt.Errorf("%w: %s: missing target", ErrInvalidSnapshot, name)
	case !inFamily(p.Target):
		return ConnProbe{}, fmt.Errorf("%w: %s: target %s has wrong family", ErrInvalidSnapshot, name, p.Target)
	case err != nil:
		return ConnProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, name, err)
	case p.Status == probe.StatusOK && p.Ping.PacketsReceived == 0:
		return ConnProbe{}, fmt.Errorf("%w: %s: status ok but no packets received", ErrInvalidSnapshot, name)
	}

	return p, nil
}

// SnapshotRepository persists snapshots.
type SnapshotRepository interface {
	SavePublicNetSnapshot(ctx context.Context, snap *Snapshot) error
}
