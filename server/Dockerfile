# Build stage
FROM golang:1.22-bookworm AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build the server with security flags
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /server ./server/cmd/server

# Runtime stage - using Google's distroless Debian image
# This is a minimal, hardened image with no shell, package manager, or other tools
FROM gcr.io/distroless/static-debian12:nonroot

# Copy binary
COPY --from=builder /server /server

# Run as non-root user (distroless nonroot user has UID 65532)
USER nonroot:nonroot

# Cloud Run expects port 8080
ENV HTTP_PORT=8080
ENV GRPC_PORT=50051

EXPOSE 8080 50051

ENTRYPOINT ["/server"]
