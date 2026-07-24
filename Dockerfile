# Two-stage build → distroless. CGO is disabled: the gateway is pure Go, and the
# embedding model runs in Ollama (a separate service), so there is no native
# dependency to link.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /gateway ./cmd/gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=build /gateway /gateway
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/gateway"]
