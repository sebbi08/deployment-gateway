# syntax=docker/dockerfile:1.28
# Changing the version requires updating both checksums below.
ARG COMPOSE_VERSION=v5.7.0

FROM node:26.13.0-bookworm-slim@sha256:bc0a2afd3b74ce12222fc8bab99271e66257f021078f5f4a5d687a77d280c1d9 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
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
ADD --chmod=0755 --checksum=sha256:47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0 https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64 /docker-compose

FROM scratch AS compose-arm64
ARG COMPOSE_VERSION
ADD --chmod=0755 --checksum=sha256:42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-aarch64 /docker-compose

FROM compose-${TARGETARCH} AS compose

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=build /out/deploy-gateway /usr/local/bin/deploy-gateway
ENV DATA_DIR=/data DOCKER_CONFIG=/docker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/deploy-gateway"]
CMD ["serve"]
