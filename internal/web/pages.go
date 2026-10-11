package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/history"
)

const (
	pageChangeLimit = 100
	minutesPerHour  = 60
)

type rangeOption struct {
	Value  string
	Label  string
	Active bool
}

// row is one line of a card: a probe or an address. Values are shown one
// per line.
type row struct {
	Label   string
	Status  string
	Values  []string
	Detail  string
	Message string
}

type card struct {
	Title    string
	Subtitle string
	Enabled  bool
	Taken    string
	TakenAt  string
	TakenISO string
	Rows     []row
}

type changeRow struct {
	Time     string
	DateTime string
	Source   string
	Subject  string
	Kind     string
	Text     string
	Message  string
}

type overview struct {
	Range       string
	Ranges      []rangeOption
	Cards       []card
	Changes     []changeRow
	Truncated   bool
	Limit       int
	GeneratedAt string
	// ServerTime lets the page's script correct for a browser clock that
	// differs from the server's when it counts relative times up.
	ServerTime string
}

func (s *Server) handleOverviewPage(w http.ResponseWriter, r *http.Request) {
	s.renderOverview(w, r, "page")
}

func (s *Server) handleOverviewFragment(w http.ResponseWriter, r *http.Request) {
	s.renderOverview(w, r, "overview")
}

func (s *Server) renderOverview(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	view, err := s.overview(ctx, r.URL.Query().Get("range"))
	if err != nil {
		s.logger.ErrorContext(ctx, "building overview failed", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Render into a buffer, so a template error does not leave half a page.
	var buf bytes.Buffer

	err = s.templates.ExecuteTemplate(&buf, name, view)
	if err != nil {
		s.logger.ErrorContext(ctx, "rendering overview failed", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buf.WriteTo(w)
}

func (s *Server) overview(ctx context.Context, rangeParam string) (overview, error) {
	rangeValue := rangeParam

	rng, err := parseRange(rangeValue)
	if err != nil || rangeValue == "" {
		rng, rangeValue = defaultRange, "24h"
	}

	public, err := latest(ctx, s.snapshots.PublicNetSnapshots)
	if err != nil {
		return overview{}, fmt.Errorf("read latest public net snapshot: %w", err)
	}

	private, err := latest(ctx, s.snapshots.PrivateNetSnapshots)
	if err != nil {
		return overview{}, fmt.Errorf("read latest private net snapshot: %w", err)
	}

	domains, err := latest(ctx, s.snapshots.DomainIPSnapshots)
	if err != nil {
		return overview{}, fmt.Errorf("read latest domain ip snapshot: %w", err)
	}

	now := s.now()

	events, err := s.changes.Changes(ctx, now.Add(-rng), pageChangeLimit)
	if err != nil {
		return overview{}, fmt.Errorf("list changes: %w", err)
	}

	changes := make([]changeRow, 0, len(events))
	for _, e := range events {
		changes = append(changes, s.changeRow(e, now))
	}

	return overview{
		Range:       rangeValue,
		Ranges:      rangeOptions(rangeValue),
		Cards:       []card{s.publicCard(public, now), s.privateCard(private, now), s.domainCard(domains, now)},
		Changes:     changes,
		Truncated:   len(events) == pageChangeLimit,
		Limit:       pageChangeLimit,
		GeneratedAt: now.In(s.location).Format("15:04:05"),
		ServerTime:  now.UTC().Format(time.RFC3339Nano),
	}, nil
}

func rangeOptions(active string) []rangeOption {
	options := []rangeOption{
		{Value: "1h", Label: "1 hour", Active: false},
		{Value: "24h", Label: "24 hours", Active: false},
		{Value: "7d", Label: "7 days", Active: false},
	}

	for i := range options {
		options[i].Active = options[i].Value == active
	}

	return options
}

func (s *Server) newCard(title string, enabled bool, taken time.Time, now time.Time) card {
	c := card{Title: title, Subtitle: "", Enabled: enabled, Taken: "", TakenAt: "", TakenISO: "", Rows: nil}
	if !taken.IsZero() {
		c.Taken = ago(now.Sub(taken))
		c.TakenAt = taken.In(s.location).Format(time.DateTime)
		c.TakenISO = taken.UTC().Format(time.RFC3339Nano)
	}

	return c
}

func (s *Server) publicCard(snap *publicnet.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("Public network", s.features.PublicNet, time.Time{}, now)
	}

	c := s.newCard("Public network", s.features.PublicNet, snap.Timestamp, now)

	for _, p := range []struct {
		label string
		probe publicnet.IPAddrProbe
	}{{"Public IPv4", snap.IPv4}, {"Public IPv6", snap.IPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result,
			join(sourceHost(p.probe.Source), durationText(p.probe.Result)), addrString(p.probe.Addr)))
	}

	for _, p := range []struct {
		label string
		probe publicnet.ConnProbe
	}{{"Internet (IPv4)", snap.ConnIPv4}, {"Internet (IPv6)", snap.ConnIPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result, pingText(p.probe.Result, p.probe.Ping),
			addrString(p.probe.Target)))
	}

	return c
}

func (s *Server) privateCard(snap *privatenet.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("Private network", s.features.PrivateNet, time.Time{}, now)
	}

	c := s.newCard("Private network", s.features.PrivateNet, snap.Timestamp, now)

	switch {
	case snap.Addrs.Status != probe.StatusOK:
		c.Rows = append(c.Rows, resultRow("Private addresses", snap.Addrs.Result, ""))
	case len(snap.Addrs.Addrs) == 0:
		c.Rows = append(c.Rows, resultRow("Private addresses", snap.Addrs.Result, "", "none"))
	}

	if snap.Addrs.Status == probe.StatusOK {
		for _, a := range snap.Addrs.Addrs {
			c.Rows = append(c.Rows, resultRow(a.Interface, snap.Addrs.Result, "", a.Prefix.String()))
		}
	}

	for _, p := range []struct {
		label string
		probe privatenet.GatewayProbe
	}{{"Gateway (IPv4)", snap.GatewayIPv4}, {"Gateway (IPv6)", snap.GatewayIPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result, pingText(p.probe.Result, p.probe.Ping),
			addrString(p.probe.Addr)))
	}

	return c
}

func (s *Server) domainCard(snap *domainip.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("Domains", s.features.DomainIP, time.Time{}, now)
	}

	c := s.newCard("Domains", s.features.DomainIP, snap.Timestamp, now)
	c.Subtitle = "via " + snap.Resolver

	for _, l := range snap.Lookups {
		addrs := make([]string, 0, len(l.Addrs))
		for _, a := range l.Addrs {
			addrs = append(addrs, a.String())
		}

		c.Rows = append(c.Rows, resultRow(l.Domain, l.Result, durationText(l.Result), addrs...))
	}

	return c
}

func resultRow(label string, r probe.Result, detail string, values ...string) row {
	message := ""
	if r.Status == probe.StatusFailed || r.Status == probe.StatusUnavailable {
		message = r.Message
	}

	values = slices.DeleteFunc(values, func(v string) bool { return v == "" })
	if len(values) == 0 {
		values = []string{"–"}
	}

	return row{Label: label, Status: r.Status.String(), Values: values, Detail: detail, Message: message}
}

func addrString(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}

	return addr.String()
}

// sourceHost shortens a lookup endpoint to its host.
func sourceHost(source string) string {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" {
		return source
	}

	return u.Host
}

func durationText(r probe.Result) string {
	if r.Status == probe.StatusDisabled {
		return ""
	}

	return msText(r.Duration)
}

func pingText(r probe.Result, p probe.PingStats) string {
	if r.Status == probe.StatusDisabled || p.PacketsSent == 0 {
		return ""
	}

	text := fmt.Sprintf("%d/%d replies", p.PacketsReceived, p.PacketsSent)
	if p.PacketsReceived > 0 {
		text += fmt.Sprintf(" · avg %s · jitter %s", msText(p.RoundTripAvg), msText(p.Jitter))
	}

	return text
}

func msText(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/millisPerSecond)
}

func join(parts ...string) string {
	var nonEmpty []string

	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}

	return strings.Join(nonEmpty, " · ")
}

// ago describes how long ago something happened, for example "12 s ago".
// static/app.js formats relative times the same way in the browser.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < hoursPerDay*time.Hour:
		return fmt.Sprintf("%d h %d min ago", int(d.Hours()), int(d.Minutes())%minutesPerHour)
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours())/hoursPerDay)
	}
}

var sourceLabels = map[history.Source]string{ //nolint:gochecknoglobals // Read-only lookup table.
	history.SourcePublicNet:  "Public",
	history.SourcePrivateNet: "Private",
	history.SourceDomainIP:   "Domains",
}

var subjectLabels = map[string]string{ //nolint:gochecknoglobals // Read-only lookup table.
	"ipv4":         "Public IPv4",
	"ipv6":         "Public IPv6",
	"conn_ipv4":    "Internet (IPv4)",
	"conn_ipv6":    "Internet (IPv6)",
	"addrs":        "Private addresses",
	"gateway_ipv4": "Gateway (IPv4)",
	"gateway_ipv6": "Gateway (IPv6)",
}

func (s *Server) changeRow(e history.Event, now time.Time) changeRow {
	local := e.Time.In(s.location)

	timeText := local.Format("15:04:05")
	if y, m, d := local.Date(); y != now.In(s.location).Year() || m != now.In(s.location).Month() ||
		d != now.In(s.location).Day() {
		timeText = local.Format("Jan 2 15:04")
	}

	subject := e.Subject
	if label, ok := subjectLabels[e.Subject]; ok && e.Source != history.SourceDomainIP {
		subject = label
	}

	var text string

	switch e.Kind {
	case history.KindChanged, history.KindStatus:
		text = e.Old + " → " + e.New
	case history.KindAdded:
		text = "added " + e.New
	case history.KindRemoved:
		text = "removed " + e.Old
	}

	return changeRow{
		Time:     timeText,
		DateTime: e.Time.UTC().Format(time.RFC3339),
		Source:   sourceLabels[e.Source],
		Subject:  subject,
		Kind:     string(e.Kind),
		Text:     text,
		Message:  e.Message,
	}
}
