package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"lifeops/internal/store"
)

type Store interface {
	UpsertCalendarEvents(ctx context.Context, events []store.CalendarEvent) (store.UpsertStats, error)
}

type Source struct {
	ID  string
	URL string
}

type Config struct {
	Sources     []Source
	PastDays    int
	FutureDays  int
	Interval    time.Duration
	HTTPTimeout time.Duration
}

type Syncer struct {
	store  Store
	cfg    Config
	client *http.Client
}

func NewSyncer(store Store, cfg Config) *Syncer {
	local := cfg
	if local.PastDays < 0 {
		local.PastDays = 0
	}
	if local.FutureDays <= 0 {
		local.FutureDays = 14
	}
	if local.Interval <= 0 {
		local.Interval = 30 * time.Minute
	}
	timeout := local.HTTPTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	return &Syncer{
		store:  store,
		cfg:    local,
		client: client,
	}
}

func (s *Syncer) Run(ctx context.Context) error {
	if err := s.SyncOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.SyncOnce(ctx); err != nil {
				log.Printf("calendar sync: %v", err)
			}
		}
	}
}

func (s *Syncer) SyncOnce(ctx context.Context) error {
	if len(s.cfg.Sources) == 0 {
		return fmt.Errorf("no calendar sources configured")
	}
	var errs []error
	for _, src := range s.cfg.Sources {
		if err := s.syncSource(ctx, src); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncSource(ctx context.Context, src Source) error {
	cal, err := s.fetchCalendar(ctx, src.URL)
	if err != nil {
		return fmt.Errorf("fetch calendar: %w", err)
	}
	now := time.Now().UTC()
	windowStart := now.AddDate(0, 0, -s.cfg.PastDays)
	windowEnd := now.AddDate(0, 0, s.cfg.FutureDays)
	events := collectEvents(cal, src.ID, windowStart, windowEnd)
	stats, err := s.store.UpsertCalendarEvents(ctx, events)
	if err != nil {
		return fmt.Errorf("upsert events: %w", err)
	}
	log.Printf("calendar sync: %s events=%d inserted=%d updated=%d", src.ID, len(events), stats.Inserted, stats.Updated)
	return nil
}

func (s *Syncer) fetchCalendar(ctx context.Context, url string) (*ics.Calendar, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	cal, err := ics.ParseCalendar(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse calendar: %w", err)
	}
	return cal, nil
}

func collectEvents(cal *ics.Calendar, calendarID string, windowStart, windowEnd time.Time) []store.CalendarEvent {
	var events []store.CalendarEvent
	for _, ve := range cal.Events() {
		if isCancelled(ve) {
			continue
		}
		ev, err := buildEvent(calendarID, ve)
		if err != nil {
			log.Printf("calendar sync: skip event in %s: %v", calendarID, err)
			continue
		}
		if ev.End.Before(windowStart) || ev.Start.After(windowEnd) {
			continue
		}
		events = append(events, ev)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Start.Equal(events[j].Start) {
			return events[i].ID < events[j].ID
		}
		return events[i].Start.Before(events[j].Start)
	})
	return events
}

func buildEvent(calendarID string, ve *ics.VEvent) (store.CalendarEvent, error) {
	uid := strings.TrimSpace(ve.Id())
	if uid == "" {
		if prop := ve.GetProperty(ics.ComponentPropertyUniqueId); prop != nil {
			uid = strings.TrimSpace(prop.Value)
		}
	}
	if uid == "" {
		return store.CalendarEvent{}, fmt.Errorf("missing uid")
	}
	start, err := ve.GetStartAt()
	allDay := false
	if err != nil {
		start, err = ve.GetAllDayStartAt()
		if err != nil {
			return store.CalendarEvent{}, fmt.Errorf("start time: %w", err)
		}
		allDay = true
	}
	if !allDay {
		allDay = isAllDayProperty(ve.GetProperty(ics.ComponentPropertyDtStart))
	}
	end, err := ve.GetEndAt()
	if err != nil {
		if allDay {
			end = start.Add(24 * time.Hour)
		} else {
			end = start
		}
	}
	if end.Before(start) {
		end = start
	}
	title := strings.TrimSpace(propertyValue(ve, ics.ComponentPropertySummary))
	description := strings.TrimSpace(propertyValue(ve, ics.ComponentPropertyDescription))
	location := strings.TrimSpace(propertyValue(ve, ics.ComponentPropertyLocation))
	startUTC := start.UTC()
	endUTC := end.UTC()
	event := store.CalendarEvent{
		ID:          makeEventID(calendarID, uid, startUTC),
		CalendarID:  calendarID,
		EventUID:    uid,
		Title:       title,
		Description: description,
		Location:    location,
		Start:       startUTC,
		End:         endUTC,
		AllDay:      allDay,
		Raw:         buildRawPayload(ve),
	}
	if updated := extractUpdated(ve); updated != nil {
		ts := updated.UTC()
		event.SourceUpdatedAt = &ts
	}
	return event, nil
}

func isCancelled(ve *ics.VEvent) bool {
	status := strings.TrimSpace(propertyValue(ve, ics.ComponentPropertyStatus))
	return strings.EqualFold(status, "CANCELLED")
}

func propertyValue(ve *ics.VEvent, prop ics.ComponentProperty) string {
	p := ve.GetProperty(prop)
	if p == nil {
		return ""
	}
	return p.Value
}

func isAllDayProperty(prop *ics.IANAProperty) bool {
	if prop == nil {
		return false
	}
	return prop.GetValueType() == ics.ValueDataTypeDate
}

func extractUpdated(ve *ics.VEvent) *time.Time {
	if ts, err := ve.GetLastModifiedAt(); err == nil && !ts.IsZero() {
		return &ts
	}
	if ts, err := ve.GetDtStampTime(); err == nil && !ts.IsZero() {
		return &ts
	}
	return nil
}

func buildRawPayload(ve *ics.VEvent) json.RawMessage {
	payload := map[string]any{
		"uid": ve.Id(),
	}
	if prop := ve.GetProperty(ics.ComponentPropertySummary); prop != nil {
		payload["summary_raw"] = prop.Value
	}
	if prop := ve.GetProperty(ics.ComponentPropertyDescription); prop != nil {
		payload["description_raw"] = prop.Value
	}
	if prop := ve.GetProperty(ics.ComponentPropertyLocation); prop != nil {
		payload["location_raw"] = prop.Value
	}
	if prop := ve.GetProperty(ics.ComponentPropertyOrganizer); prop != nil {
		payload["organizer"] = prop.Value
	}
	if prop := ve.GetProperty(ics.ComponentPropertyUrl); prop != nil {
		payload["url"] = prop.Value
	}
	if prop := ve.GetProperty(ics.ComponentPropertyDtStart); prop != nil {
		payload["dtstart"] = rawProperty(prop)
	}
	if prop := ve.GetProperty(ics.ComponentPropertyDtEnd); prop != nil {
		payload["dtend"] = rawProperty(prop)
	}
	if prop := ve.GetProperty(ics.ComponentPropertyLastModified); prop != nil {
		payload["last_modified"] = rawProperty(prop)
	}
	if prop := ve.GetProperty(ics.ComponentPropertyDtstamp); prop != nil {
		payload["dtstamp"] = rawProperty(prop)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

func rawProperty(prop *ics.IANAProperty) map[string]any {
	if prop == nil {
		return nil
	}
	out := map[string]any{
		"value": prop.Value,
	}
	if len(prop.ICalParameters) > 0 {
		out["params"] = prop.ICalParameters
	}
	return out
}

func makeEventID(calendarID, uid string, start time.Time) string {
	return fmt.Sprintf("%s:%s:%s", calendarID, uid, start.UTC().Format(time.RFC3339Nano))
}
