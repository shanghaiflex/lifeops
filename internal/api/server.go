package api

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-chi/chi/v5"
	"lifeops/internal/config"
	"lifeops/internal/health"
	"lifeops/internal/llm"
	"lifeops/internal/store"
	"lifeops/internal/telegram"
	"lifeops/internal/worker"
)

//go:embed openapi.yaml
var openapiFS embed.FS

const agentNotificationCooldown = 2 * time.Hour

type Server struct {
	service                  *health.Service
	router                   *chi.Mux
	store                    *store.Store
	cfg                      *config.Config
	provider                 llm.Provider
	lastAgentNotification    map[string]time.Time
	lastAgentNotificationMux sync.RWMutex
}

func NewServer(service *health.Service, store *store.Store, cfg *config.Config, provider llm.Provider) (*Server, error) {
	specBytes, err := openapiFS.ReadFile("openapi.yaml")
	if err != nil {
		return nil, fmt.Errorf("read openapi: %w", err)
	}
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData(specBytes)
	if err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	spec.Servers = nil
	openapiRouter, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, fmt.Errorf("build openapi router: %w", err)
	}
	router := chi.NewRouter()
	router.Use(openapiMiddleware(openapiRouter))
	s := &Server{
		service:               service,
		router:                router,
		store:                 store,
		cfg:                   cfg,
		provider:              provider,
		lastAgentNotification: make(map[string]time.Time),
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.router.Post("/v1/ingest/health/workouts", s.handleWorkouts)
	s.router.Post("/v1/ingest/health/sleep", s.handleSleep)
	s.router.Post("/v1/ingest/health/metrics", s.handleMetrics)
	s.router.Post("/v1/worker/run", s.handleWorkerRun)
}

func (s *Server) Router() http.Handler {
	return s.router
}

func (s *Server) handleWorkouts(w http.ResponseWriter, r *http.Request) {
	var payload health.WorkoutsBatch
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stats, err := s.service.IngestWorkouts(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("ingest workouts: %d items (%d inserted, %d updated), %d deletions", len(payload.Items), stats.Inserted, stats.Updated, len(payload.Deleted))

	// Trigger coach agent notification if new workouts were added
	if stats.Inserted > 0 || stats.Updated > 0 {
		s.triggerAgentNotification("coach")
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSleep(w http.ResponseWriter, r *http.Request) {
	var payload health.SleepBatch
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stats, err := s.service.IngestSleep(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("ingest sleep: %d items (%d inserted, %d updated), %d deletions", len(payload.Items), stats.Inserted, stats.Updated, len(payload.Deleted))

	// Trigger sleep agent notification if new sleep sessions were added
	if stats.Inserted > 0 || stats.Updated > 0 {
		s.triggerAgentNotification("sleep")
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var payload health.MetricsBatch
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stats, err := s.service.IngestMetrics(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	logMetricSamples(payload.Items)
	log.Printf("ingest metrics: %d items (%d inserted, %d updated), %d deletions", len(payload.Items), stats.Inserted, stats.Updated, len(payload.Deleted))
	w.WriteHeader(http.StatusOK)
}

type workerRunRequest struct {
	Agent string `json:"agent"`
}

func (s *Server) handleWorkerRun(w http.ResponseWriter, r *http.Request) {
	var payload workerRunRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agentName := strings.TrimSpace(payload.Agent)
	if agentName == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("agent is required"))
		return
	}
	agentCfg, ok := s.cfg.TelegramAgents[agentName]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("agent %q not found", agentName))
		return
	}
	token := strings.TrimSpace(agentCfg.TelegramToken)
	if token == "" && len(s.cfg.TelegramBotTokens) > 0 {
		if fallback, ok := s.cfg.TelegramBotTokens[agentName]; ok {
			token = strings.TrimSpace(fallback)
		}
	}
	if token == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("telegram token missing for agent %q", agentName))
		return
	}
	sender, err := telegram.NewBotSender(token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("telegram sender: %w", err))
		return
	}
	workerInstance := worker.New(s.store, s.provider, sender, agentCfg, s.cfg.ChatHistoryLimit, agentCfg.DefaultChatIDs)
	workerInstance.EnableDebug(worker.TriggerManual)
	if err := workerInstance.RunOnce(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"agent":  agentName,
	})
}

func decodeJSON(r *http.Request, dest interface{}) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(dest)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": err.Error(),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func openapiMiddleware(validator routers.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/healthz") {
				next.ServeHTTP(w, r)
				return
			}
			var bodyBytes []byte
			if r.Body != nil {
				bodyBytes, _ = io.ReadAll(r.Body)
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			route, pathParams, err := validator.FindRoute(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			input := &openapi3filter.RequestValidationInput{
				Request:    r,
				PathParams: pathParams,
				Route:      route,
			}
			if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if bodyBytes != nil {
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func Healthz(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

const metricLogPreviewLimit = 10

func logMetricSamples(items []health.Metric) {
	if len(items) == 0 {
		return
	}
	limit := len(items)
	if limit > metricLogPreviewLimit {
		limit = metricLogPreviewLimit
	}
	for i := 0; i < limit; i++ {
		item := items[i]
		log.Printf(
			"ingest metrics detail %d/%d: id=%s kind=%s start=%s end=%s value=%g unit=%s",
			i+1,
			len(items),
			item.ID,
			item.Kind,
			item.Start.Format(time.RFC3339),
			item.End.Format(time.RFC3339),
			item.Value,
			item.Unit,
		)
	}
	if len(items) > metricLogPreviewLimit {
		log.Printf("ingest metrics detail truncated: logged first %d of %d items", metricLogPreviewLimit, len(items))
	}
}

// triggerAgentNotification sends a proactive message from the specified agent based on new data
func (s *Server) triggerAgentNotification(agentName string) {
	// Check cooldown to prevent spamming messages
	s.lastAgentNotificationMux.RLock()
	lastNotification, exists := s.lastAgentNotification[agentName]
	s.lastAgentNotificationMux.RUnlock()

	if exists && time.Since(lastNotification) < agentNotificationCooldown {
		log.Printf("trigger %s notification: skipped due to cooldown (last notification %v ago)",
			agentName, time.Since(lastNotification).Round(time.Minute))
		return
	}

	// Update last notification time
	s.lastAgentNotificationMux.Lock()
	s.lastAgentNotification[agentName] = time.Now()
	s.lastAgentNotificationMux.Unlock()

	go func() {
		agentCfg, ok := s.cfg.TelegramAgents[agentName]
		if !ok {
			log.Printf("trigger %s notification: agent not configured", agentName)
			return
		}

		token := strings.TrimSpace(agentCfg.TelegramToken)
		if token == "" && len(s.cfg.TelegramBotTokens) > 0 {
			if fallback, ok := s.cfg.TelegramBotTokens[agentName]; ok {
				token = strings.TrimSpace(fallback)
			}
		}

		// For nutrition agent, use review token if available
		if strings.EqualFold(agentName, "nutrition") {
			if reviewToken := strings.TrimSpace(agentCfg.NutritionReviewTelegramToken); reviewToken != "" {
				token = reviewToken
			}
		}

		if token == "" {
			log.Printf("trigger %s notification: no telegram token configured", agentName)
			return
		}

		sender, err := telegram.NewBotSender(token)
		if err != nil {
			log.Printf("trigger %s notification: create sender: %v", agentName, err)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		workerInstance := worker.New(s.store, s.provider, sender, agentCfg, s.cfg.ChatHistoryLimit, agentCfg.DefaultChatIDs)
		workerInstance.EnableDebug(worker.TriggerDataIngestion)
		if err := workerInstance.RunOnce(ctx); err != nil {
			log.Printf("trigger %s notification: %v", agentName, err)
			return
		}

		log.Printf("trigger %s notification: sent successfully", agentName)
	}()
}
