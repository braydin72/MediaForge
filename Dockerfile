# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BUILD_NUMBER=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X github.com/braydin72/mediaforge/internal/version.Build=${BUILD_NUMBER}" -o mediaforge ./cmd/mediaforge

# Runtime stage - linuxserver/ffmpeg already has s6-overlay + hardware accel
FROM linuxserver/ffmpeg:latest

# Copy the binary
COPY --from=builder /app/mediaforge /usr/local/bin/mediaforge

# Copy s6-overlay service definition
COPY root/ /

# Restore s6-overlay entrypoint (linuxserver/ffmpeg overrides it)
ENTRYPOINT ["/init"]

EXPOSE 8080
VOLUME /config /media