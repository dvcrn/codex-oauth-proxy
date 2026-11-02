# Multi-stage build for Go application
FROM --platform=$BUILDPLATFORM golang:1.25.7-alpine AS builder

# Set working directory
WORKDIR /app

# Copy dependency manifests
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Cross-compile on the build platform for each target architecture
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid=" \
    -o codex-oauth-proxy ./cmd/codex-oauth-proxy

# Final stage - minimal image
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1001 appgroup && \
    adduser -D -u 1001 -G appgroup appuser

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/codex-oauth-proxy .

# Change ownership to non-root user
RUN chown appuser:appgroup codex-oauth-proxy

# Switch to non-root user
USER appuser

# Default server port
EXPOSE 9879

# Run the binary
CMD ["./codex-oauth-proxy"]
