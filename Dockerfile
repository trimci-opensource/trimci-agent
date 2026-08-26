# Build a release-equivalent image from source:
#   docker build -t trimci-agent --build-arg VERSION=0.0.0-local .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/trimci/agent/internal/version.Version=${VERSION}" \
    -o /out/trimci-agent ./cmd/trimci-agent

# distroless/static: CA certificates and a nonroot user, no shell, no package
# manager, nothing else — the whole image is the agent binary.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/trimci-agent /trimci-agent
ENTRYPOINT ["/trimci-agent"]
