FROM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS builder
# GOPROXY build-arg: dev.sh injects the host's go env GOPROXY; the default
# proxy.golang.org is unreachable from CN hosts.
ARG GOPROXY
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY deploy/fixtures/cmd ./deploy/fixtures/cmd
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/release-fixture ./deploy/fixtures/cmd/server

FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
USER nonroot:nonroot
WORKDIR /data
COPY --from=builder /out/release-fixture /release-fixture
COPY deploy/fixtures/static /static
EXPOSE 8088
ENTRYPOINT ["/release-fixture"]
