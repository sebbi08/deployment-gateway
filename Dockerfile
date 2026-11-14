# syntax=docker/dockerfile:1.28
# Changing the version requires updating both checksums below.
ARG COMPOSE_VERSION=v5.7.1

FROM node:26.13.0-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
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
ADD --chmod=0755 --checksum=sha256:7758048ad38e580f22559603312874fec8bb638d021a9e64182385fc17cc12a2 https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64 /docker-compose

FROM scratch AS compose-arm64
ARG COMPOSE_VERSION
ADD --chmod=0755 --checksum=sha256:15861535d14e26555736b49376c95e14bfd94285366c8446bfac7156a972e76f https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-aarch64 /docker-compose

FROM compose-${TARGETARCH} AS compose

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=build /out/deploy-gateway /usr/local/bin/deploy-gateway
ENV DATA_DIR=/data DOCKER_CONFIG=/docker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/deploy-gateway"]
CMD ["serve"]
