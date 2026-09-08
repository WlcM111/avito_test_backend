# Short-lived job that applies the embedded migrations of one service database.
# Dependencies are downloaded from the module proxy on a cold cache.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY migrations ./migrations
COPY cmd/migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.21
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/migrate /usr/local/bin/migrate
ENTRYPOINT ["/usr/local/bin/migrate"]
