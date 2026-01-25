FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN go build -o /bin/lifeops ./cmd/lifeops

FROM alpine:3.20
RUN adduser -D -g '' appuser
USER appuser
WORKDIR /app
COPY --from=build /bin/lifeops /bin/lifeops
COPY db/migrations ./db/migrations
COPY internal/api/openapi.yaml ./internal/api/openapi.yaml
COPY openapi.yaml ./openapi.yaml
ENTRYPOINT ["/bin/lifeops"]
