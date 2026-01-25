package api

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/kin-openapi/kin-openapi/openapi3"
	"github.com/kin-openapi/kin-openapi/openapi3filter"
	"github.com/kin-openapi/kin-openapi/routers/chi"
	"lifeops/internal/health"
)

//go:embed openapi.yaml
var openapiFS embed.FS

type Server struct {
	service *health.Service
	apiKey  string
	router  *chi.Mux
}

func NewServer(service *health.Service, apiKey string) (*Server, error) {
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
	router := chi.NewRouter()
	router.Use(openapiMiddleware(spec))
	s := &Server{
		service: service,
		apiKey:  apiKey,
		router:  router,
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
	if err := s.service.IngestWorkouts(r.Context(), payload); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSleep(w http.ResponseWriter, r *http.Request) {
	var payload health.SleepBatch
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.IngestSleep(r.Context(), payload); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var payload health.MetricsBatch
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.IngestMetrics(r.Context(), payload); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
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

func openapiMiddleware(spec *openapi3.T) func(http.Handler) http.Handler {
	validator, _ := chi.NewRouterFromOpenAPI(spec)
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

func APIKeyMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/healthz") {
				next.ServeHTTP(w, r)
				return
			}
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("X-Api-Key") != apiKey {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid api key"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) WithAPIKey() http.Handler {
	return APIKeyMiddleware(s.apiKey)(s.router)
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
