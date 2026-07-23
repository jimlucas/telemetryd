# syntax=docker/dockerfile:1
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/telemetryd ./cmd/telemetryd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/telemetryd /telemetryd
USER nonroot:nonroot
EXPOSE 50051 8080
ENTRYPOINT ["/telemetryd"]
CMD ["--grpc-listen=:50051", "--http-listen=:8080", "--log-json=true"]
