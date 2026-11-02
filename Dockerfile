# Build stage
FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION}" -o /app/doexam ./app/server

# Runtime stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates && adduser -D -H doexam \
    && mkdir -p /app/data/storage && chown -R doexam /app/data
WORKDIR /app
ENV STORAGE_DIR=/app/data/storage
COPY --from=builder /app/doexam .
COPY --from=builder /app/migrations ./migrations
USER doexam
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["./doexam"]
