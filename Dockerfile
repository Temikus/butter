# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Cache dependencies before copying source (layer cache optimization)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
      -X github.com/temikus/butter/internal/version.Version=${VERSION} \
      -X github.com/temikus/butter/internal/version.Commit=${COMMIT} \
      -X github.com/temikus/butter/internal/version.Date=${DATE}" \
    -o butter ./cmd/butter/

# Stage 2: Runtime
# distroless/static includes CA certificates and timezone data for HTTPS calls.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/butter /butter

EXPOSE 8080

ENTRYPOINT ["/butter"]
CMD ["-config", "/config.yaml"]
