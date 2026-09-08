# Build stage. Dependencies are downloaded from the module proxy, so the build
# needs network access on a cold cache; the layers are ordered so that only a
# change of go.mod/go.sum invalidates the download.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY gen ./gen
COPY pkg ./pkg
COPY internal ./internal
COPY migrations ./migrations
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
        -o /out/inventory-service ./cmd/inventory-service

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata curl && \
    adduser -D -u 10001 app
USER app
COPY --from=build /out/inventory-service /usr/local/bin/inventory-service
EXPOSE 8080 9090 9091
ENTRYPOINT ["/usr/local/bin/inventory-service"]
