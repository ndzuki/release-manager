FROM golang:1.27.3@sha256:1ad9754cfcca40bdb57424ff4ad9ca754f5d5e4660944a9b266da90f8a6853a5 AS builder
# GOPROXY build-arg: dev.sh injects the host's go env GOPROXY; the default
# proxy.golang.org is unreachable from CN hosts.
ARG GOPROXY
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY deploy/fixtures/cmd ./deploy/fixtures/cmd
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/release-fixture ./deploy/fixtures/cmd/server

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
USER nonroot:nonroot
WORKDIR /data
COPY --from=builder /out/release-fixture /release-fixture
COPY deploy/fixtures/static /static
EXPOSE 8088
ENTRYPOINT ["/release-fixture"]
