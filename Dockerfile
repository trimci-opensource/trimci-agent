# Build a release-equivalent image from source:
#   docker build -t trimci-agent --build-arg VERSION=0.0.0-local .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/trimci-opensource/trimci-agent/internal/version.Version=${VERSION}" \
    -o /out/trimci-agent ./cmd/trimci-agent

# distroless/static: CA certificates and a nonroot user, no shell, no package
# manager, nothing else — the whole image is the agent binary.
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
LABEL org.opencontainers.image.source="https://github.com/trimci-opensource/trimci-agent"
LABEL org.opencontainers.image.description="TrimCI Agent - GitLab CI push agent for private networks (outbound-only, zero code access)"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="TrimCI"
COPY --from=build /out/trimci-agent /trimci-agent
ENTRYPOINT ["/trimci-agent"]
