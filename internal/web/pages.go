package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
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
	// overviewRefresh and historyRefresh are how often HTMX reloads the
	// overview and the history, in HTMX's trigger syntax. The history
	// compares many snapshots, so it refreshes less often.
	overviewRefresh = "5s"
	historyRefresh  = "15s"
)

const (
	pageOverview = "overview"
	pageHistory  = "history"
)

type rangeOption struct {
	Value  string
	Label  string
	Active bool
}

// value is one value of a row, for example an address. Copy is the text the
// copy button copies; an empty Copy shows no button. Suffix is shown after
// Text in a muted color, for example the prefix length of an address.
type value struct {
	Text   string
	Suffix string
	Copy   string
}

// row is one line of a card: a probe, an address or a domain. Values are
// shown one per line, Details as small facts below them.
type row struct {
	Label     string
	CopyLabel bool
	Status    probe.Status
	Values    []value
	Details   []string
	Message   string
}

type card struct {
	// ID names the card's icon and anchors it, for example "public".
	ID       string
	Title    string
	Subtitle string
	Enabled  bool
	// Summary sums up the rows' statuses, for example "all ok" or
	// "1 failed"; SummaryStatus is the status it is colored with.
	Summary       string
	SummaryStatus string
	Taken         string
	TakenAt       string
	TakenISO      string
	Rows          []row
}

type changeRow struct {
	Time     string
	DateTime string
	Source   string
	SourceID string
	Subject  string
	// CopySubject tells whether the subject is a domain worth copying.
	CopySubject bool
	// Kind is the [history.Kind]. For "status", Old and New are statuses,
	// otherwise addresses.
	Kind    string
	Old     string
	New     string
	Message string
}

type overview struct {
	Refresh string
	Cards   []card
	// ServerTime lets the page's script correct for a browser clock that
	// differs from the server's when it counts relative times up.
	ServerTime string
}

type historyView struct {
	Refresh    string
	Range      string
	Ranges     []rangeOption
	Changes    []changeRow
	Truncated  bool
	Limit      int
	ServerTime string
}

// page is a whole HTML page: the frame with the navigation and either the
// overview or the history.
type page struct {
	Page        string
	Title       string
	GeneratedAt string
	ServerTime  string
	Overview    *overview
	History     *historyView
}

func (s *Server) handleOverviewPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "page", func(ctx context.Context, now time.Time) (any, error) {
		view, err := s.overview(ctx, now)

		return s.page(pageOverview, "Overview", now, &view, nil), err
	})
}

func (s *Server) handleOverviewFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, pageOverview, func(ctx context.Context, now time.Time) (any, error) {
		return s.overview(ctx, now)
	})
}

func (s *Server) handleHistoryPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "page", func(ctx context.Context, now time.Time) (any, error) {
		view, err := s.history(ctx, now, r.URL.Query().Get("range"))

		return s.page(pageHistory, "History", now, nil, &view), err
	})
}

func (s *Server) handleHistoryFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, pageHistory, func(ctx context.Context, now time.Time) (any, error) {
		return s.history(ctx, now, r.URL.Query().Get("range"))
	})
}

// render executes the template name with the data that build returns.
func (s *Server) render(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	build func(ctx context.Context, now time.Time) (any, error),
) {
	ctx := r.Context()

	data, err := build(ctx, s.now())
	if err != nil {
		s.logger.ErrorContext(ctx, "building page failed", slog.String("template", name), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Render into a buffer, so a template error does not leave half a page.
	var buf bytes.Buffer

	err = s.templates.ExecuteTemplate(&buf, name, data)
	if err != nil {
		s.logger.ErrorContext(ctx, "rendering page failed", slog.String("template", name), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buf.WriteTo(w)
}

func (s *Server) page(name, title string, now time.Time, ov *overview, hv *historyView) page {
	return page{
		Page:        name,
		Title:       title,
		GeneratedAt: now.In(s.location).Format("15:04:05"),
		ServerTime:  serverTime(now),
		Overview:    ov,
		History:     hv,
	}
}

func serverTime(now time.Time) string {
	return now.UTC().Format(time.RFC3339Nano)
}

func (s *Server) overview(ctx context.Context, now time.Time) (overview, error) {
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

	return overview{
		Refresh:    overviewRefresh,
		Cards:      []card{s.publicCard(public, now), s.privateCard(private, now), s.domainCard(domains, now)},
		ServerTime: serverTime(now),
	}, nil
}

func (s *Server) history(ctx context.Context, now time.Time, rangeParam string) (historyView, error) {
	rangeValue := rangeParam

	rng, err := parseRange(rangeValue)
	if err != nil || rangeValue == "" {
		rng, rangeValue = defaultRange, "24h"
	}

	events, err := s.changes.Changes(ctx, now.Add(-rng), pageChangeLimit)
	if err != nil {
		return historyView{}, fmt.Errorf("list changes: %w", err)
	}

	changes := make([]changeRow, 0, len(events))
	for _, e := range events {
		changes = append(changes, s.changeRow(e, now))
	}

	return historyView{
		Refresh:    historyRefresh,
		Range:      rangeValue,
		Ranges:     rangeOptions(rangeValue),
		Changes:    changes,
		Truncated:  len(events) == pageChangeLimit,
		Limit:      pageChangeLimit,
		ServerTime: serverTime(now),
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

func (s *Server) newCard(id, title string, enabled bool, taken time.Time, now time.Time) card {
	c := card{
		ID:            id,
		Title:         title,
		Subtitle:      "",
		Enabled:       enabled,
		Summary:       "",
		SummaryStatus: "",
		Taken:         "",
		TakenAt:       "",
		TakenISO:      "",
		Rows:          nil,
	}

	if !taken.IsZero() {
		c.Taken = ago(now.Sub(taken))
		c.TakenAt = taken.In(s.location).Format(time.DateTime)
		c.TakenISO = taken.UTC().Format(time.RFC3339Nano)
	}

	return c
}

func (s *Server) publicCard(snap *publicnet.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("public", "Public network", s.features.PublicNet, time.Time{}, now)
	}

	c := s.newCard("public", "Public network", s.features.PublicNet, snap.Timestamp, now)

	for _, p := range []struct {
		label string
		probe publicnet.IPAddrProbe
	}{{"Public IPv4", snap.IPv4}, {"Public IPv6", snap.IPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result,
			details(sourceHost(p.probe.Source), durationText(p.probe.Result)), addrValue(p.probe.Addr)))
	}

	for _, p := range []struct {
		label string
		probe publicnet.ConnProbe
	}{{"Internet (IPv4)", snap.ConnIPv4}, {"Internet (IPv6)", snap.ConnIPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result, pingDetails(p.probe.Result, p.probe.Ping),
			addrValue(p.probe.Target)))
	}

	return summarize(c)
}

func (s *Server) privateCard(snap *privatenet.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("private", "Private network", s.features.PrivateNet, time.Time{}, now)
	}

	c := s.newCard("private", "Private network", s.features.PrivateNet, snap.Timestamp, now)

	switch {
	case snap.Addrs.Status != probe.StatusOK:
		c.Rows = append(c.Rows, resultRow("Private addresses", snap.Addrs.Result, nil))
	case len(snap.Addrs.Addrs) == 0:
		c.Rows = append(c.Rows, resultRow("Private addresses", snap.Addrs.Result, nil,
			value{Text: "none", Suffix: "", Copy: ""}))
	}

	if snap.Addrs.Status == probe.StatusOK {
		for _, a := range snap.Addrs.Addrs {
			c.Rows = append(c.Rows, resultRow(a.Interface, snap.Addrs.Result, nil, prefixValue(a.Prefix)))
		}
	}

	for _, p := range []struct {
		label string
		probe privatenet.GatewayProbe
	}{{"Gateway (IPv4)", snap.GatewayIPv4}, {"Gateway (IPv6)", snap.GatewayIPv6}} {
		c.Rows = append(c.Rows, resultRow(p.label, p.probe.Result, pingDetails(p.probe.Result, p.probe.Ping),
			addrValue(p.probe.Addr)))
	}

	return summarize(c)
}

func (s *Server) domainCard(snap *domainip.Snapshot, now time.Time) card {
	if snap == nil {
		return s.newCard("domains", "Domains", s.features.DomainIP, time.Time{}, now)
	}

	c := s.newCard("domains", "Domains", s.features.DomainIP, snap.Timestamp, now)
	c.Subtitle = "via " + snap.Resolver

	for _, l := range snap.Lookups {
		values := make([]value, 0, len(l.Addrs))
		for _, a := range l.Addrs {
			values = append(values, addrValue(a))
		}

		r := resultRow(l.Domain, l.Result, details(durationText(l.Result)), values...)
		r.CopyLabel = true
		c.Rows = append(c.Rows, r)
	}

	return summarize(c)
}

// summarize sets the card's summary from the statuses of its rows: the most
// severe status that is not ok and how many rows have it, or "all ok".
func summarize(c card) card {
	counts := make(map[probe.Status]int)
	for _, r := range c.Rows {
		counts[r.Status]++
	}

	for _, status := range []probe.Status{probe.StatusFailed, probe.StatusUnavailable, probe.StatusUnknown} {
		if n := counts[status]; n > 0 {
			c.Summary = fmt.Sprintf("%d %s", n, status)
			c.SummaryStatus = status.String()

			return c
		}
	}

	switch {
	case counts[probe.StatusOK] > 0:
		c.Summary, c.SummaryStatus = "all ok", probe.StatusOK.String()
	case len(c.Rows) > 0:
		c.Summary, c.SummaryStatus = "disabled", probe.StatusDisabled.String()
	}

	return c
}

func resultRow(label string, r probe.Result, details []string, values ...value) row {
	message := ""
	if r.Status == probe.StatusFailed || r.Status == probe.StatusUnavailable {
		message = r.Message
	}

	var shown []value

	for _, v := range values {
		if v.Text != "" {
			shown = append(shown, v)
		}
	}

	if len(shown) == 0 {
		shown = []value{{Text: "–", Suffix: "", Copy: ""}}
	}

	return row{
		Label:     label,
		CopyLabel: false,
		Status:    r.Status,
		Values:    shown,
		Details:   details,
		Message:   message,
	}
}

// addrValue shows an address with a copy button; an invalid address shows
// nothing.
func addrValue(addr netip.Addr) value {
	if !addr.IsValid() {
		return value{Text: "", Suffix: "", Copy: ""}
	}

	return value{Text: addr.String(), Suffix: "", Copy: addr.String()}
}

// prefixValue shows an interface address with its prefix length; the copy
// button copies only the address.
func prefixValue(prefix netip.Prefix) value {
	return value{Text: prefix.Addr().String(), Suffix: fmt.Sprintf("/%d", prefix.Bits()), Copy: prefix.Addr().String()}
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

func pingDetails(r probe.Result, p probe.PingStats) []string {
	if r.Status == probe.StatusDisabled || p.PacketsSent == 0 {
		return nil
	}

	facts := []string{fmt.Sprintf("%d/%d replies", p.PacketsReceived, p.PacketsSent)}
	if p.PacketsReceived > 0 {
		facts = append(facts, "avg "+msText(p.RoundTripAvg), "jitter "+msText(p.Jitter))
	}

	return facts
}

func msText(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/millisPerSecond)
}

// details drops the empty parts.
func details(parts ...string) []string {
	var facts []string

	for _, p := range parts {
		if p != "" {
			facts = append(facts, p)
		}
	}

	return facts
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

var sourceIDs = map[history.Source]string{ //nolint:gochecknoglobals // Read-only lookup table.
	history.SourcePublicNet:  "public",
	history.SourcePrivateNet: "private",
	history.SourceDomainIP:   "domains",
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
	today := now.In(s.location)

	timeText := local.Format("15:04:05")
	if y, m, d := local.Date(); y != today.Year() || m != today.Month() || d != today.Day() {
		timeText = local.Format("Jan 2 15:04")
	}

	subject := e.Subject
	if label, ok := subjectLabels[e.Subject]; ok && e.Source != history.SourceDomainIP {
		subject = label
	}

	return changeRow{
		Time:        timeText,
		DateTime:    e.Time.UTC().Format(time.RFC3339),
		Source:      sourceLabels[e.Source],
		SourceID:    sourceIDs[e.Source],
		Subject:     subject,
		CopySubject: e.Source == history.SourceDomainIP,
		Kind:        string(e.Kind),
		Old:         e.Old,
		New:         e.New,
		Message:     e.Message,
	}
}
