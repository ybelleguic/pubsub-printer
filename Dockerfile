# Runtime image is scratch plus the CA bundle, so it does not depend on gcr.io.
# That registry is not always reachable from a non-default GCP universe.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pubsub-printer ./cmd/pubsub-printer

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/pubsub-printer /pubsub-printer
USER 65532:65532
ENTRYPOINT ["/pubsub-printer"]
