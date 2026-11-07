# DoExam server (API). The binary embeds its migrations (`doexam migrate up`).
# This image serves the API only; the release repo (doexam) builds the all-in-one image
# with the frontend embedded and the separate exam/panel images.
#
#   docker build -t doexam-server .
#   docker run --rm -e DATABASE_URL=... doexam-server migrate up
#   docker run -p 8080:8080 -e DATABASE_URL=... doexam-server

FROM golang:1.26-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/doexam ./app/server

FROM alpine:3.22
RUN apk --no-cache add ca-certificates tzdata && adduser -D -H -u 10001 doexam \
    && mkdir -p /app/data/storage && chown -R doexam /app/data
WORKDIR /app
ENV STORAGE_DIR=/app/data/storage
COPY --from=builder /out/doexam /usr/local/bin/doexam
USER 10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["doexam"]
CMD ["serve"]
