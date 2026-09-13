# SubAI gateway — multi-stage build. Final image contains only the server
# binary, migrations, embedded rules and the built admin UI.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY rules rules
COPY migrations migrations
RUN CGO_ENABLED=0 go build -o /out/subai-server ./cmd/server

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
