FROM golang:1.26.3-alpine AS builder
WORKDIR /build

# Copy dependency manifests first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download && \
    go install github.com/pressly/goose/v3/cmd/goose@latest

# Copy source and build — this layer re-runs on every source change.
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN go build \
  -ldflags "-X github.com/GMISWE/ieops-aihub/internal/version.Version=${VERSION} \
            -X github.com/GMISWE/ieops-aihub/internal/version.GitCommit=${GIT_COMMIT} \
            -X github.com/GMISWE/ieops-aihub/internal/version.BuildTime=${BUILD_TIME}" \
  -o /usr/local/bin/aihub ./cmd/aihub/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget
# Copy binaries
COPY --from=builder /usr/local/bin/aihub /usr/local/bin/aihub
COPY --from=builder /go/bin/goose /usr/local/bin/goose
# Copy SQL migrations so goose can find them at /migrations
COPY --from=builder /build/internal/db/migrations /migrations
# Entrypoint: migrate-up → goose up, else → aihub server
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/docker-entrypoint.sh"]
