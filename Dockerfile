FROM golang:1.22-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./ 2>/dev/null || true
RUN if [ -f go.mod ]; then go mod download; fi

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o proxy-ui main.go

FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates && update-ca-certificates

COPY --from=builder /app/proxy-ui /app/proxy-ui
COPY config.json /app/config.json

ENV CONFIG_FILE=/app/config.json

EXPOSE 8000

CMD ["/app/proxy-ui"]

