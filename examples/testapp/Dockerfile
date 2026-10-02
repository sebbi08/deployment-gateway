# syntax=docker/dockerfile:1.7
# Build from the repository root:  docker build -f examples/testapp/Dockerfile .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
COPY examples/testapp ./examples/testapp
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/testapp ./examples/testapp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/testapp /testapp
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/testapp"]
