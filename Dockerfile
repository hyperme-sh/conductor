FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath -ldflags='-s -w' -o /out/conductor ./cmd/conductor

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/lib/conductor && chown 65532:65532 /var/lib/conductor
ARG SOURCE_REVISION
LABEL org.opencontainers.image.source="https://github.com/hyperme-sh/conductor" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
COPY --from=build /out/conductor /usr/local/bin/conductor
USER 65532:65532
WORKDIR /var/lib/conductor
ENTRYPOINT ["/usr/local/bin/conductor"]
