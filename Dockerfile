# Static build, then a small image that runs as a normal user and keeps its database in /data.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gateway .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 gateway && mkdir /data && chown gateway /data
COPY --from=build /gateway /usr/local/bin/gateway
USER gateway
WORKDIR /data
# Reachable from the proxy in front of it. Set GATEWAY_KEY, a provider key, and ALLOWED_HOSTS to your public hostname.
ENV LISTEN=0.0.0.0:8080 DB_PATH=/data/gateway.db
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/admin >/dev/null || exit 1
CMD ["gateway"]
