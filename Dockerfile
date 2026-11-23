# Step 1: Build the binary using the official Go image
FROM golang:1.27.4-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a lightweight statically linked binary for Linux
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o climate-app ./cmd/app

# Step 2: Final minimal runtime image
FROM alpine:3.22

# Run as an unprivileged user
RUN addgroup -S app && adduser -S -G app app

WORKDIR /app

# Copy the compiled binary from the previous stage
COPY --from=builder /build/climate-app .

USER app

# Port for Prometheus metrics and /healthz
EXPOSE 2112

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:2112/healthz >/dev/null || exit 1

ENTRYPOINT ["./climate-app"]
