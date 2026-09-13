# SubAI gateway — multi-stage build. Final image contains only the server
# binary, migrations, embedded rules and the built admin UI.
FROM golang:1.27-alpine AS build
WORKDIR /src
ARG APP_VERSION
ARG VCS_REF
ARG BUILD_TIME
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./
COPY cmd cmd
COPY internal internal
COPY rules rules
COPY migrations migrations
COPY web/package.json web/package.json
COPY web/src web/src
RUN set -eu; \
    app_version="${APP_VERSION:-$(tr -d '\r\n' < VERSION)}"; \
    revision="${VCS_REF:-$(find cmd internal rules migrations web -type f -exec sha256sum {} + | sort | sha256sum | cut -c1-12)}"; \
    built_at="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"; \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X subai/internal/buildinfo.Version=${app_version} -X subai/internal/buildinfo.Revision=${revision} -X subai/internal/buildinfo.BuiltAt=${built_at}" \
      -o /out/subai-server ./cmd/server

FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D subai
WORKDIR /app
COPY --from=build /out/subai-server ./subai-server
COPY --from=build /src/migrations ./migrations
COPY --from=web /web/dist ./web/dist
USER subai
EXPOSE 8080
ENTRYPOINT ["./subai-server"]
