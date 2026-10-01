# syntax=docker/dockerfile:1.7
FROM node:24.18.0-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/deploy-gateway ./cmd/deploy-gateway

FROM gcr.io/distroless/static-debian12
# Standalone Docker Compose; checksum from the release's docker-compose-linux-x86_64.sha256
ARG COMPOSE_VERSION=v5.5.1
ARG COMPOSE_SHA256=db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576
ADD --chmod=0755 --checksum=sha256:${COMPOSE_SHA256} https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64 /usr/local/bin/docker-compose
COPY --from=build /out/deploy-gateway /usr/local/bin/deploy-gateway
ENV DATA_DIR=/data DOCKER_CONFIG=/docker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/deploy-gateway"]
CMD ["serve"]
