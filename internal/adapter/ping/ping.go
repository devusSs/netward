// Package ping measures connectivity to a host with ICMP echo requests.
//
// It wraps [probing] and reduces a run to the counts and round-trip times
// netward stores. By default it sends unprivileged pings, which work without
// root on macOS and on Linux when the net.ipv4.ping_group_range sysctl
// includes the process's group. Privileged pings use raw sockets and need root
// or CAP_NET_RAW.
//
// [probing]: https://pkg.go.dev/github.com/prometheus-community/pro-bing
package ping

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

const (
	// DefaultCount is the default number of echo requests per run.
	DefaultCount = 4
	// DefaultInterval is the default wait between echo requests.
	DefaultInterval = time.Second
	// DefaultTimeout is the default limit for a whole run.
	DefaultTimeout = 5 * time.Second
)

var (
	// ErrUnavailable is returned by [Pinger.Ping] when the host has no route
	// to the target, for example an IPv6 target on a network without IPv6.
	ErrUnavailable = errors.New("target unreachable")
	// ErrNotPermitted is returned by [Pinger.Ping] when the process may not
	// open the ICMP socket. Unprivileged pings need the net.ipv4.ping_group_range
	// sysctl on Linux, privileged pings need root or CAP_NET_RAW.
	ErrNotPermitted = errors.New("icmp not permitted")
)

// Config configures a [Pinger].
type Config struct {
	// Count is the number of echo requests per run.
	Count int
	// Interval is the wait between echo requests.
	Interval time.Duration
	// Timeout limits a whole run, including waiting for the last reply. It
	// must leave room to send all requests: Interval * (Count - 1) < Timeout.
	Timeout time.Duration
	// Privileged sends raw ICMP instead of unprivileged datagram pings.
	Privileged bool
}

// DefaultConfig returns a config with the default count, interval and timeout
// that sends unprivileged pings.
func DefaultConfig() Config {
	return Config{
		Count:      DefaultCount,
		Interval:   DefaultInterval,
		Timeout:    DefaultTimeout,
		Privileged: false,
	}
}

// Stats summarizes one ping run. The round-trip fields are zero when no reply
// was received.
type Stats struct {
	// PacketsSent is the number of echo requests sent.
	PacketsSent int
	// PacketsReceived is the number of echo replies received, without
	// duplicates.
	PacketsReceived int
	// RoundTripMin is the shortest round-trip time.
	RoundTripMin time.Duration
	// RoundTripMax is the longest round-trip time.
	RoundTripMax time.Duration
	// RoundTripAvg is the mean round-trip time.
	RoundTripAvg time.Duration
	// Jitter is the mean absolute difference between consecutive round-trip
	// times, as in RFC 3550 but without smoothing. It is zero with fewer than
	// two replies.
	Jitter time.Duration
}

// Pinger pings targets with a fixed [Config]. It is safe for concurrent use;
// each call to [Pinger.Ping] uses its own socket.
type Pinger struct {
	cfg Config
}

// New returns a pinger for cfg after checking that its values are usable.
func New(cfg Config) (*Pinger, error) {
	switch {
	case cfg.Count <= 0:
		return nil, fmt.Errorf("count must be positive, got %d", cfg.Count)
	case cfg.Interval <= 0:
		return nil, fmt.Errorf("interval must be positive, got %s", cfg.Interval)
	case cfg.Timeout <= cfg.Interval*time.Duration(cfg.Count-1):
		return nil, fmt.Errorf(
			"timeout %s is too short to send %d packets every %s",
			cfg.Timeout, cfg.Count, cfg.Interval,
		)
	}

	return &Pinger{cfg: cfg}, nil
}

// Config returns the pinger's configuration.
func (p *Pinger) Config() Config {
	return p.cfg
}

// Ping sends echo requests to target and returns the statistics of the run.
// IPv4-mapped IPv6 targets are pinged over IPv4.
//
// Lost replies are not an error: a run where every request goes unanswered
// returns stats with zero packets received and a nil error. The error wraps
// [ErrUnavailable] when the host has no route to target and [ErrNotPermitted]
// when the ICMP socket cannot be opened.
func (p *Pinger) Ping(ctx context.Context, target netip.Addr) (Stats, error) {
	target = target.Unmap()
	if !target.IsValid() {
		return Stats{}, errors.New("invalid target address")
	}

	// sendErr is written by the pinger's send loop and read after
	// RunWithContext returns, which waits for that loop to finish.
	var sendErr error

	pinger := probing.New(target.String())
	pinger.SetIPAddr(&net.IPAddr{IP: target.AsSlice(), Zone: target.Zone()})
	pinger.SetPrivileged(p.cfg.Privileged)
	pinger.SetLogger(probing.NoopLogger{})
	pinger.Count = p.cfg.Count
	pinger.Interval = p.cfg.Interval
	pinger.Timeout = p.cfg.Timeout
	pinger.RecordRtts = true
	pinger.RecordTTLs = false
	pinger.OnSendError = func(_ *probing.Packet, err error) {
		if sendErr == nil {
			sendErr = err
		}
	}

	err := pinger.RunWithContext(ctx)
	stats := pinger.Statistics()

	// After the first packet, pro-bing only reports send errors through
	// OnSendError. They matter only when no reply made it back.
	if err == nil && stats.PacketsRecv == 0 {
		err = sendErr
	}

	if err != nil {
		return Stats{}, classify(target, err)
	}

	return Stats{
		PacketsSent:     stats.PacketsSent,
		PacketsReceived: stats.PacketsRecv,
		RoundTripMin:    stats.MinRtt,
		RoundTripMax:    stats.MaxRtt,
		RoundTripAvg:    stats.AvgRtt,
		Jitter:          jitter(stats.Rtts),
	}, nil
}

func classify(target netip.Addr, err error) error {
	switch {
	case isUnavailable(err):
		return fmt.Errorf("%w: ping %s: %w", ErrUnavailable, target, err)
	case isNotPermitted(err):
		return fmt.Errorf("%w: ping %s: %w", ErrNotPermitted, target, err)
	}

	return fmt.Errorf("ping %s: %w", target, err)
}

// isUnavailable reports whether err means the host has no route to the
// target, as opposed to the target not answering.
func isUnavailable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}

// isNotPermitted reports whether err means the ICMP socket was refused.
func isNotPermitted(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPROTONOSUPPORT)
}

// jitter returns the mean absolute difference between consecutive round-trip
// times.
func jitter(rtts []time.Duration) time.Duration {
	pairs := len(rtts) - 1
	if pairs <= 0 {
		return 0
	}

	var sum time.Duration

	for i, rtt := range rtts[1:] {
		sum += (rtt - rtts[i]).Abs()
	}

	return sum / time.Duration(pairs)
}
