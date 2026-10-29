package web

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/history"
)

const (
	defaultRange = 24 * time.Hour
	// maxRange bounds how many snapshots a single change list compares.
	maxRange           = 7 * 24 * time.Hour
	defaultChangeLimit = 100
	maxChangeLimit     = 1000
	millisPerSecond    = 1000
	hoursPerDay        = 24
)

// farFuture is the exclusive upper bound when reading the latest snapshots.
func farFuture() time.Time {
	return time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
}

// parseRange parses a time range like "24h", "90m" or "7d", up to 7 days.
// An empty string means 24 hours.
func parseRange(value string) (time.Duration, error) {
	if value == "" {
		return defaultRange, nil
	}

	var (
		d   time.Duration
		err error
	)

	if days, ok := strings.CutSuffix(value, "d"); ok {
		var n int

		n, err = strconv.Atoi(days)
		d = time.Duration(n) * hoursPerDay * time.Hour
	} else {
		d, err = time.ParseDuration(value)
	}

	if err != nil || d <= 0 || d > maxRange {
		return 0, fmt.Errorf("range %q must be a positive duration like 24h or 7d of at most 7d", value)
	}

	return d, nil
}

type errorJSON struct {
	Error string `json:"error"`
}

type resultJSON struct {
	Status     probe.Status `json:"status"`
	Message    string       `json:"message,omitempty"`
	StartedAt  time.Time    `json:"started_at,omitzero"`
	DurationMS float64      `json:"duration_ms"`
}

type pingJSON struct {
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	RTTMinMS float64 `json:"rtt_min_ms"`
	RTTMaxMS float64 `json:"rtt_max_ms"`
	RTTAvgMS float64 `json:"rtt_avg_ms"`
	JitterMS float64 `json:"jitter_ms"`
}

type ipProbeJSON struct {
	resultJSON `json:",inline"`

	Source string     `json:"source,omitempty"`
	Addr   netip.Addr `json:"addr,omitzero"`
}

type connProbeJSON struct {
	resultJSON `json:",inline"`

	Method publicnet.ConnMethod `json:"method,omitzero"`
	Target netip.Addr           `json:"target,omitzero"`
	Ping   pingJSON             `json:"ping"`
}

type publicNetJSON struct {
	ID        uuid.UUID     `json:"id"`
	Timestamp time.Time     `json:"timestamp"`
	IPv4      ipProbeJSON   `json:"ipv4"`
	IPv6      ipProbeJSON   `json:"ipv6"`
	ConnIPv4  connProbeJSON `json:"conn_ipv4"`
	ConnIPv6  connProbeJSON `json:"conn_ipv6"`
}

type interfaceAddrJSON struct {
	Interface string       `json:"interface"`
	Prefix    netip.Prefix `json:"prefix"`
}

type addrsProbeJSON struct {
	resultJSON `json:",inline"`

	Addrs []interfaceAddrJSON `json:"addrs"`
}

type gatewayProbeJSON struct {
	resultJSON `json:",inline"`

	Addr netip.Addr `json:"addr,omitzero"`
	Ping pingJSON   `json:"ping"`
}

type privateNetJSON struct {
	ID          uuid.UUID        `json:"id"`
	Timestamp   time.Time        `json:"timestamp"`
	Addrs       addrsProbeJSON   `json:"addrs"`
	GatewayIPv4 gatewayProbeJSON `json:"gateway_ipv4"`
	GatewayIPv6 gatewayProbeJSON `json:"gateway_ipv6"`
}

type lookupJSON struct {
	resultJSON `json:",inline"`

	Domain string       `json:"domain"`
	Addrs  []netip.Addr `json:"addrs"`
}

type domainIPJSON struct {
	ID        uuid.UUID    `json:"id"`
	Timestamp time.Time    `json:"timestamp"`
	Resolver  string       `json:"resolver"`
	Lookups   []lookupJSON `json:"lookups"`
}

type eventJSON struct {
	Time    time.Time      `json:"time"`
	Source  history.Source `json:"source"`
	Subject string         `json:"subject"`
	Kind    history.Kind   `json:"kind"`
	Old     string         `json:"old,omitempty"`
	New     string         `json:"new,omitempty"`
	Message string         `json:"message,omitempty"`
}

type changesJSON struct {
	Since  time.Time   `json:"since"`
	Events []eventJSON `json:"events"`
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / millisPerSecond
}

func toResultJSON(r probe.Result) resultJSON {
	return resultJSON{Status: r.Status, Message: r.Message, StartedAt: r.StartedAt, DurationMS: millis(r.Duration)}
}

func toPingJSON(p probe.PingStats) pingJSON {
	return pingJSON{
		Sent:     p.PacketsSent,
		Received: p.PacketsReceived,
		RTTMinMS: millis(p.RoundTripMin),
		RTTMaxMS: millis(p.RoundTripMax),
		RTTAvgMS: millis(p.RoundTripAvg),
		JitterMS: millis(p.Jitter),
	}
}

func toPublicNetJSON(snap *publicnet.Snapshot) publicNetJSON {
	ip := func(p publicnet.IPAddrProbe) ipProbeJSON {
		return ipProbeJSON{resultJSON: toResultJSON(p.Result), Source: p.Source, Addr: p.Addr}
	}
	conn := func(p publicnet.ConnProbe) connProbeJSON {
		return connProbeJSON{
			resultJSON: toResultJSON(p.Result),
			Method:     p.Method,
			Target:     p.Target,
			Ping:       toPingJSON(p.Ping),
		}
	}

	return publicNetJSON{
		ID:        snap.ID,
		Timestamp: snap.Timestamp,
		IPv4:      ip(snap.IPv4),
		IPv6:      ip(snap.IPv6),
		ConnIPv4:  conn(snap.ConnIPv4),
		ConnIPv6:  conn(snap.ConnIPv6),
	}
}

func toPrivateNetJSON(snap *privatenet.Snapshot) privateNetJSON {
	addrs := make([]interfaceAddrJSON, 0, len(snap.Addrs.Addrs))
	for _, a := range snap.Addrs.Addrs {
		addrs = append(addrs, interfaceAddrJSON{Interface: a.Interface, Prefix: a.Prefix})
	}

	gateway := func(p privatenet.GatewayProbe) gatewayProbeJSON {
		return gatewayProbeJSON{resultJSON: toResultJSON(p.Result), Addr: p.Addr, Ping: toPingJSON(p.Ping)}
	}

	return privateNetJSON{
		ID:          snap.ID,
		Timestamp:   snap.Timestamp,
		Addrs:       addrsProbeJSON{resultJSON: toResultJSON(snap.Addrs.Result), Addrs: addrs},
		GatewayIPv4: gateway(snap.GatewayIPv4),
		GatewayIPv6: gateway(snap.GatewayIPv6),
	}
}

func toDomainIPJSON(snap *domainip.Snapshot) domainIPJSON {
	lookups := make([]lookupJSON, 0, len(snap.Lookups))
	for _, l := range snap.Lookups {
		addrs := l.Addrs
		if addrs == nil {
			addrs = []netip.Addr{}
		}

		lookups = append(lookups, lookupJSON{resultJSON: toResultJSON(l.Result), Domain: l.Domain, Addrs: addrs})
	}

	return domainIPJSON{ID: snap.ID, Timestamp: snap.Timestamp, Resolver: snap.Resolver, Lookups: lookups}
}

func (s *Server) writeJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	err := json.MarshalWrite(w, v)
	if err != nil {
		s.logger.WarnContext(ctx, "writing json response failed", slog.Any("error", err))
	}
}

func (s *Server) writeJSONError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	s.writeJSON(ctx, w, status, errorJSON{Error: msg})
}

// internalError logs err and answers with a generic message, so internal
// details stay in the logs.
func (s *Server) internalError(ctx context.Context, w http.ResponseWriter, err error) {
	s.logger.ErrorContext(ctx, "handling request failed", slog.Any("error", err))
	s.writeJSONError(ctx, w, http.StatusInternalServerError, "internal error")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
}

// latest returns the newest snapshot or nil if there is none.
func latest[S any](
	ctx context.Context,
	list func(ctx context.Context, from, to time.Time, limit int) ([]*S, error),
) (*S, error) {
	snaps, err := list(ctx, time.Time{}, farFuture(), 1)
	if err != nil || len(snaps) == 0 {
		return nil, err
	}

	return snaps[0], nil
}

func serveLatest[S, J any](
	s *Server,
	w http.ResponseWriter,
	r *http.Request,
	name string,
	enabled bool,
	list func(ctx context.Context, from, to time.Time, limit int) ([]*S, error),
	convert func(*S) J,
) {
	ctx := r.Context()

	snap, err := latest(ctx, list)

	switch {
	case err != nil:
		s.internalError(ctx, w, fmt.Errorf("read latest %s snapshot: %w", name, err))
	case snap == nil && !enabled:
		s.writeJSONError(ctx, w, http.StatusNotFound, name+" is disabled")
	case snap == nil:
		s.writeJSONError(ctx, w, http.StatusNotFound, "no "+name+" snapshot yet")
	default:
		s.writeJSON(ctx, w, http.StatusOK, convert(snap))
	}
}

func (s *Server) handlePublicNetLatest(w http.ResponseWriter, r *http.Request) {
	serveLatest(s, w, r, "publicnet", s.features.PublicNet, s.snapshots.PublicNetSnapshots, toPublicNetJSON)
}

func (s *Server) handlePrivateNetLatest(w http.ResponseWriter, r *http.Request) {
	serveLatest(s, w, r, "privatenet", s.features.PrivateNet, s.snapshots.PrivateNetSnapshots, toPrivateNetJSON)
}

func (s *Server) handleDomainIPLatest(w http.ResponseWriter, r *http.Request) {
	serveLatest(s, w, r, "domainip", s.features.DomainIP, s.snapshots.DomainIPSnapshots, toDomainIPJSON)
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	rng, err := parseRange(query.Get("since"))
	if err != nil {
		s.writeJSONError(ctx, w, http.StatusBadRequest, err.Error())

		return
	}

	limit := defaultChangeLimit

	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxChangeLimit {
			s.writeJSONError(ctx, w, http.StatusBadRequest,
				fmt.Sprintf("limit %q must be a number between 1 and %d", value, maxChangeLimit))

			return
		}
	}

	since := s.now().Add(-rng)

	events, err := s.changes.Changes(ctx, since, limit)
	if err != nil {
		s.internalError(ctx, w, fmt.Errorf("list changes: %w", err))

		return
	}

	result := changesJSON{Since: since.UTC(), Events: make([]eventJSON, 0, len(events))}
	for _, e := range events {
		result.Events = append(result.Events, eventJSON(e))
	}

	s.writeJSON(ctx, w, http.StatusOK, result)
}
