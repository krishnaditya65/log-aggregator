FROM golang:1.25-alpine AS build
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY go.mod ./
COPY go.sum* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/aggregator ./cmd/aggregator
RUN go build -o /out/agent ./cmd/agent
RUN go build -o /out/alerting-service ./cmd/alerting-service
RUN go build -o /out/ingest-gateway ./cmd/ingest-gateway

FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/aggregator /usr/local/bin/aggregator
COPY --from=build /out/agent /usr/local/bin/cla-agent
COPY --from=build /out/alerting-service /usr/local/bin/alerting-service
COPY --from=build /out/ingest-gateway /usr/local/bin/ingest-gateway
# HTTP API (v1+v2), OTLP gRPC, OTLP/HTTP (now served by ingest-gateway),
# standalone alerting-service
EXPOSE 8080 4317 4318 8083
ENTRYPOINT ["aggregator"]
