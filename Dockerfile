# Multi-stage Dockerfile – builds any cmd/* binary.
# Usage: docker build --build-arg SERVICE=apigw -t prompted-apigw .
FROM golang:1.26-alpine AS builder

ARG SERVICE

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /app ./cmd/${SERVICE}

# ---- runtime ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app /app
COPY migrations /migrations
COPY samples /samples

ENTRYPOINT ["/app"]
