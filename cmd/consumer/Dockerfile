# Step 1: Build the Go application
FROM golang:1.23-alpine AS builder

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod go.sum ./

# Download all dependencies (this will be cached if go.mod and go.sum have not changed)
RUN go mod download

# Copy the rest of the application code
COPY main.go .

# Build the Go app (use the appropriate build command for your app)
RUN go build -o main .

# Step 2: Create a small image with only the built binary
FROM alpine:latest

# Create a non-root user and group
RUN addgroup -S skylo && adduser -S skylo -G skylo

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy the Pre-built binary file from the builder stage
COPY --from=builder /app/main .

# Change ownership of the binary to the non-root user
RUN chown skylo:skylo /app/main

# Switch to the non-root user
USER skylo

# Command to run the executable
CMD ["./main"]
