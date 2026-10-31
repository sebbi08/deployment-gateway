# syntax=docker/dockerfile:1.7
# Changing the version requires updating both checksums below.
ARG COMPOSE_VERSION=v5.6.1

FROM node:24.18.0-bookworm-slim@sha256:6f7b03f7c2c8e2e784dcf9295400527b9b1270fd37b7e9a7285cf83b6951452d AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/deploy-gateway ./cmd/deploy-gateway

# Standalone Docker Compose for the target architecture; checksums from the
# release's docker-compose-linux-<arch>.sha256 files.
FROM scratch AS compose-amd64
ARG COMPOSE_VERSION
ADD --chmod=0755 --checksum=sha256:db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576 https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64 /docker-compose

FROM scratch AS compose-arm64
ARG COMPOSE_VERSION
ADD --chmod=0755 --checksum=sha256:732e3a84c1a0f67256ce80bc2598a24546b10ca05f9faa97efceb1171ece2ef7 https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-aarch64 /docker-compose

FROM compose-${TARGETARCH} AS compose

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=build /out/deploy-gateway /usr/local/bin/deploy-gateway
ENV DATA_DIR=/data DOCKER_CONFIG=/docker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/deploy-gateway"]
CMD ["serve"]
