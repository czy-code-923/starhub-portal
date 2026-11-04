FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build
FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /portal ./cmd/portal
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 portal && adduser -D -u 10001 -G portal portal
WORKDIR /app
COPY --from=backend /portal /app/portal
COPY --from=web /src/web/dist /app/web
USER 10001:10001
ENV WEB_DIR=/app/web LISTEN_ADDR=0.0.0.0:18088
EXPOSE 18088
ENTRYPOINT ["/app/portal"]
