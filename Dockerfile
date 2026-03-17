# Stage 1: Build
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /server ./cmd/server

# Stage 2: Runtime
FROM alpine:3.19
RUN apk add --no-cache ffmpeg ca-certificates
COPY --from=builder /server /server
COPY web/ /web/
WORKDIR /
EXPOSE 4242
ENTRYPOINT ["/server"]
