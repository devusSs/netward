// Package probelog logs probe outcomes the same way across netward's
// services.
package probelog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/devusSs/netward/internal/adapter/ping"
	"github.com/devusSs/netward/internal/domain/probe"
)

// Log logs a finished probe with its status and duration after attrs.
//
// Failures are warnings that include the result's message as "reason".
// Every other outcome is expected and logged at debug level; unavailable
// probes include the reason too.
func Log(ctx context.Context, logger *slog.Logger, result probe.Result, attrs ...slog.Attr) {
	attrs = append(attrs,
		slog.String("status", result.Status.String()),
		slog.Duration("duration", result.Duration),
	)

	level, msg := slog.LevelDebug, "probe finished"

	switch result.Status {
	case probe.StatusOK:
		msg = "probe succeeded"
	case probe.StatusUnavailable:
		msg = "probe unavailable"
		attrs = append(attrs, slog.String("reason", result.Message))
	case probe.StatusFailed:
		level, msg = slog.LevelWarn, "probe failed"
		attrs = append(attrs, slog.String("reason", result.Message))
	case probe.StatusDisabled:
		msg = "probe disabled"
	case probe.StatusUnknown:
	}

	logger.LogAttrs(ctx, level, msg, attrs...)
}

// PingNotPermitted logs a warning with instructions when err shows that the
// process may not send ICMP echo requests.
func PingNotPermitted(ctx context.Context, logger *slog.Logger, err error) {
	if errors.Is(err, ping.ErrNotPermitted) {
		logger.WarnContext(ctx, "icmp is not permitted for this process; "+
			"allow unprivileged pings via net.ipv4.ping_group_range or run privileged with CAP_NET_RAW")
	}
}
