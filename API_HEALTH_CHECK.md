# API Health Check - LifeOps

## Status: ✅ All Systems Operational

Date: 2026-01-29
Environment: Local Docker Compose

## Health Endpoint

### `/healthz`
```bash
curl http://localhost:8080/healthz
# Response: ok
# Status: 200 OK
# Response Time: ~1.5ms
```

**Features:**
- No authentication required
- Bypasses OpenAPI validation (optimized for speed)
- Returns plain text "ok"
- Useful for:
  - Container health checks
  - Load balancer monitoring
  - Service discovery
  - Kubernetes liveness/readiness probes

**Implementation:**
```go
// internal/api/server.go:68
s.router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("ok"))
})
```

## Available API Endpoints

### 1. **Health Check**
- **Endpoint**: `GET /healthz`
- **Purpose**: Service health monitoring
- **Response**: `200 OK` with body "ok"

### 2. **Ingest Workouts**
- **Endpoint**: `POST /v1/ingest/health/workouts`
- **Purpose**: Import workout data from Apple Watch/HealthKit
- **Content-Type**: `application/json`
- **Body**: WorkoutsBatch schema (see OpenAPI spec)

### 3. **Ingest Sleep**
- **Endpoint**: `POST /v1/ingest/health/sleep`
- **Purpose**: Import sleep session data
- **Content-Type**: `application/json`
- **Body**: SleepBatch schema (see OpenAPI spec)

### 4. **Ingest Metrics**
- **Endpoint**: `POST /v1/ingest/health/metrics`
- **Purpose**: Import health metrics (HRV, heart rate, etc.)
- **Content-Type**: `application/json`
- **Body**: MetricsBatch schema (see OpenAPI spec)

### 5. **Trigger Agent**
- **Endpoint**: `POST /v1/worker/run`
- **Purpose**: Manually trigger an agent to send a message
- **Content-Type**: `application/json`
- **Body**: `{"agent": "coach|sleep|nutrition|finance"}`
- **Response**: `{"status": "ok", "agent": "..."}`

## Testing

### Quick Health Check
```bash
curl http://localhost:8080/healthz
```

### Test Agent Trigger
```bash
# Coach agent
curl -X POST http://localhost:8080/v1/worker/run \
  -H "Content-Type: application/json" \
  -d '{"agent": "coach"}'

# Sleep agent
curl -X POST http://localhost:8080/v1/worker/run \
  -H "Content-Type: application/json" \
  -d '{"agent": "sleep"}'

# Nutrition agent
curl -X POST http://localhost:8080/v1/worker/run \
  -H "Content-Type: application/json" \
  -d '{"agent": "nutrition"}'
```

### Health Check with Timing
```bash
curl -s http://localhost:8080/healthz \
  -w "\nStatus: %{http_code}\nTime: %{time_total}s\n"
```

## Docker Compose Integration

The health check is used in docker-compose.yml for the API service:

```yaml
api:
  build: .
  command: ["api"]
  ports:
    - "8080:8080"
  healthcheck:
    test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8080/healthz"]
    interval: 30s
    timeout: 10s
    retries: 3
    start_period: 10s
```

## Monitoring

### Check if API is Running
```bash
docker-compose ps api
```

### Check API Logs
```bash
docker-compose logs -f api
```

### Test All Endpoints
```bash
# Health
curl http://localhost:8080/healthz

# Worker (triggers take 20-30s to complete)
curl -X POST http://localhost:8080/v1/worker/run \
  -H "Content-Type: application/json" \
  -d '{"agent": "coach"}'
```

## Performance Metrics

From testing on 2026-01-29:

| Endpoint | Response Time | Status |
|----------|--------------|--------|
| GET /healthz | ~1.5ms | ✅ Working |
| POST /v1/worker/run (coach) | ~24s | ✅ Working |
| POST /v1/worker/run (sleep) | ~28s | ✅ Working |
| POST /v1/worker/run (nutrition) | ~44s | ✅ Working |

**Note**: Worker endpoints take longer because they:
1. Initialize the agent
2. Query the database for data
3. Call LLM API with tools (multiple rounds)
4. Send message to Telegram
5. Save chat history

## Troubleshooting

### Health Check Fails

1. **Check if container is running:**
   ```bash
   docker-compose ps api
   ```

2. **Check API logs:**
   ```bash
   docker-compose logs api | tail -50
   ```

3. **Check if port is bound:**
   ```bash
   netstat -an | grep 8080
   # or
   lsof -i :8080
   ```

4. **Restart API service:**
   ```bash
   docker-compose restart api
   ```

### API Not Responding

1. **Check database connection:**
   ```bash
   docker-compose ps postgres
   ```

2. **Check environment variables:**
   ```bash
   docker-compose exec api env | grep POSTGRES
   ```

3. **Rebuild and restart:**
   ```bash
   docker-compose build api
   docker-compose up -d api
   ```

## API Documentation

Full OpenAPI specification available at:
- File: `internal/api/openapi.yaml`
- Schemas: See `components/schemas` section for request/response formats

## Security Notes

- `/healthz` endpoint requires no authentication (by design)
- All other endpoints validate requests against OpenAPI schema
- In production, consider:
  - Adding API authentication
  - Rate limiting
  - HTTPS/TLS
  - Firewall rules

## Next Steps

To improve health monitoring:

1. **Add more detailed health info:**
   ```json
   {
     "status": "ok",
     "timestamp": "2026-01-29T14:51:06Z",
     "version": "1.0.0",
     "database": "connected",
     "agents": ["coach", "sleep", "nutrition", "finance"]
   }
   ```

2. **Add metrics endpoint** for Prometheus/monitoring

3. **Add readiness check** that verifies:
   - Database connectivity
   - LLM provider API access
   - Telegram API access

## References

- Source code: `internal/api/server.go`
- Routes definition: Lines 67-76
- OpenAPI middleware: Lines 204-237
- Health check implementation: Line 68-71
