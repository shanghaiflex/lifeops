package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"lifeops/internal/store"
)

type stubCalendarStore struct {
	events []store.CalendarEvent
}

func (s *stubCalendarStore) UpsertCalendarEvents(_ context.Context, events []store.CalendarEvent) (store.UpsertStats, error) {
	s.events = append(s.events, events...)
	return store.UpsertStats{Inserted: len(events)}, nil
}

func TestSyncerSyncOnce(t *testing.T) {
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	end := start.Add(1 * time.Hour)
	timestamp := time.Now().UTC().Truncate(time.Second)
	icsPayload := fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:event-1\r\nDTSTAMP:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Test Meeting\r\nEND:VEVENT\r\nEND:VCALENDAR",
		timestamp.Format("20060102T150405Z"),
		start.Format("20060102T150405Z"),
		end.Format("20060102T150405Z"),
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(icsPayload))
	}))
	defer server.Close()

	storeStub := &stubCalendarStore{}
	syncer := NewSyncer(storeStub, Config{
		Sources:    []Source{{ID: "personal", URL: server.URL}},
		PastDays:   7,
		FutureDays: 30,
		Interval:   time.Minute,
	})
	syncer.client = server.Client()

	err := syncer.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, storeStub.events, 1)
	require.Equal(t, "personal", storeStub.events[0].CalendarID)
	require.Equal(t, "Test Meeting", storeStub.events[0].Title)
}
