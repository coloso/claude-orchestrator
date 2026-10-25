# syntax=docker/dockerfile:1
# To update the pin: run 'docker buildx imagetools inspect golang:1.27.3-alpine', copy the top-level digest, keep the tag.
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# To update the pin: run 'docker buildx imagetools inspect gcr.io/distroless/static-debian13:nonroot', copy the top-level digest, keep the tag.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/server /server
# Listen on all container interfaces; docker-compose.yml publishes the port on 127.0.0.1 only.
ENV LISTEN_HOST=0.0.0.0
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/server", "-healthcheck"]
ENTRYPOINT ["/server"]
