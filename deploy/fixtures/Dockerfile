FROM golang:1.26.4 AS builder
# GOPROXY build-arg: dev.sh injects the host's go env GOPROXY; the default
# proxy.golang.org is unreachable from CN hosts.
ARG GOPROXY
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY deploy/fixtures/cmd ./deploy/fixtures/cmd
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/release-fixture ./deploy/fixtures/cmd/server

FROM gcr.io/distroless/static-debian13:nonroot
USER nonroot:nonroot
WORKDIR /data
COPY --from=builder /out/release-fixture /release-fixture
COPY deploy/fixtures/static /static
EXPOSE 8088
ENTRYPOINT ["/release-fixture"]
