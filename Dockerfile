FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o 9router-proxy ./cmd/9router-proxy/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/9router-proxy /usr/local/bin/9router-proxy
EXPOSE 20128
ENTRYPOINT ["9router-proxy"]
