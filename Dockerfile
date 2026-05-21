FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY . .
RUN GOFLAGS='-mod=mod' go mod download || true
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN GOFLAGS='-mod=mod' go build \
  -ldflags "-X github.com/GMISWE/ieops-aihub/internal/version.Version=${VERSION} \
            -X github.com/GMISWE/ieops-aihub/internal/version.GitCommit=${GIT_COMMIT} \
            -X github.com/GMISWE/ieops-aihub/internal/version.BuildTime=${BUILD_TIME}" \
  -o /usr/local/bin/aihub ./cmd/aihub/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /usr/local/bin/aihub /usr/local/bin/aihub
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aihub"]
