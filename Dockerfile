FROM node:22-alpine AS frontend
WORKDIR /source/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
RUN apk add --no-cache build-base
WORKDIR /source/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/cloud .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 cloud && adduser -D -u 10001 -G cloud cloud && mkdir -p /data && chown cloud:cloud /data
WORKDIR /app
COPY --from=backend /out/cloud /app/cloud
COPY --from=frontend /source/web/dist /app/web
ENV WEB_DIST=/app/web COOKIE_SECURE=1 TZ=Asia/Shanghai
USER cloud
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q -O /dev/null http://127.0.0.1:8080/api/health || exit 1
CMD ["/app/cloud", "-listen", "0.0.0.0:8080", "-db", "/data/nicecloud.db"]
