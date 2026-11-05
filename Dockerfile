FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /aze-tourism .

FROM alpine:3.22
# CA certificates: the server calls the Kapital Bank API over HTTPS.
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 app
COPY --from=build /aze-tourism /usr/local/bin/aze-tourism
USER app
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["aze-tourism"]
