FROM golang:1.27.3-alpine AS builder

WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -buildvcs=true \
    -ldflags "-s -w" \
    -o /out/runnerd \
    ./cmd/runnerd

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/runnerd /usr/local/bin/runnerd

EXPOSE 25500
ENTRYPOINT ["/usr/local/bin/runnerd"]
CMD ["--config", "/etc/runnerd/runnerd.yaml"]
