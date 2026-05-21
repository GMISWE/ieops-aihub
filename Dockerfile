FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN go build \
  -ldflags "-X github.com/GMISWE/ieops-aihub/internal/version.Version=${VERSION} \
            -X github.com/GMISWE/ieops-aihub/internal/version.GitCommit=${GIT_COMMIT} \
            -X github.com/GMISWE/ieops-aihub/internal/version.BuildTime=${BUILD_TIME}" \
  -o /aihub ./cmd/aihub/

FROM alpine:3.20
COPY --from=builder /aihub /usr/local/bin/aihub
EXPOSE 8080
ENTRYPOINT ["/aihub"]
