# Builds the backend only - the frontend deploys separately to GitHub Pages
# (see spec Phase 9). Migrations are embedded into the binary via go:embed,
# so this image needs nothing copied in at runtime beyond the binary itself.

FROM golang:1.26.5-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatchlab-server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 dispatchlab
COPY --from=build /out/dispatchlab-server /usr/local/bin/dispatchlab-server

USER dispatchlab
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dispatchlab-server"]
