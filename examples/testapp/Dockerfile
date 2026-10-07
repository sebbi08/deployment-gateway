# syntax=docker/dockerfile:1.7
# Build from the repository root:  docker build -f examples/testapp/Dockerfile .
FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
WORKDIR /src
COPY go.mod ./
COPY examples/testapp ./examples/testapp
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/testapp ./examples/testapp

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/testapp /testapp
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/testapp"]
