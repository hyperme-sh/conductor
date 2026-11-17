FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath -ldflags='-s -w' -o /out/conductor ./cmd/conductor

FROM alpine:3.23@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2
RUN apk add --no-cache ca-certificates \
    && install -d -o 65532 -g 65532 /var/lib/conductor
ARG SOURCE_REVISION
LABEL org.opencontainers.image.source="https://github.com/hyperme-sh/conductor" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
COPY --from=build /out/conductor /usr/local/bin/conductor
USER 65532:65532
WORKDIR /var/lib/conductor
ENTRYPOINT ["/usr/local/bin/conductor"]
