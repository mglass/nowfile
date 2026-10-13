# nowfile server image. Builds every command under ./cmd (nowfile, and plan
# once it exists) and ships them on distroless as the nonroot user.

FROM golang:1.27 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is pure Go, so the binaries are static.
RUN mkdir -p /out /data && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/ /usr/local/bin/
# distroless has no shell, so /data is made in the builder and handed to
# nonroot (65532) here. A bind mount over it keeps the host's ownership.
COPY --from=builder --chown=65532:65532 /data /data
VOLUME /data

# nonroot cannot bind ports below 1024, so finger listens on 7979 inside the
# container and the host publishes it on 79: docker run -p 79:7979 ...
ENV NOWFILE_FINGER_ADDR=:7979
EXPOSE 7979 8080

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/nowfile"]
CMD []
