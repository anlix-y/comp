# Stage 1: Build the Go application
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum for dependency resolution
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the application
# Note: we build the binary from web/main.go because it's the web server entry point
RUN go build -o compressor web/main.go

# Stage 2: Final image
FROM alpine:latest

WORKDIR /app

# Install dependencies: ffmpeg, yt-dlp, nodejs (JS runtime), and ca-certificates
RUN apk add --no-cache \
    ffmpeg \
    python3 \
    nodejs \
    ca-certificates \
    curl \
    && curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod a+rwx /usr/local/bin/yt-dlp

# Copy the binary from the builder stage
COPY --from=builder /app/compressor .

# Copy static files, templates, and config
COPY --from=builder /app/web/static ./static
COPY --from=builder /app/web/templates ./templates
COPY --from=builder /app/web/config.json ./config.json

# Create storage directory
RUN mkdir -p /tmp/app && chmod 777 /tmp/app

# Expose the port (should match the port in config.json)
EXPOSE 3000

# Run the application
CMD ["./compressor"]
