FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Copy only build inputs: BuildKit then walks just these paths, so container-owned
# operator data left in the tree (data/, deploy/docker/data/) cannot break builds.
COPY cmd/ cmd/
COPY internal/ internal/
RUN cp /usr/local/go/LICENSE /GO-LICENSE
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /feedbot ./cmd/kallisti-rss
FROM alpine:3.24
RUN apk add --no-cache ca-certificates \
    && addgroup -g 65532 nonroot \
    && adduser -D -H -u 65532 -G nonroot -s /sbin/nologin nonroot
COPY --from=build /feedbot /feedbot
COPY LICENSE THIRD_PARTY.md /usr/share/doc/kallisti-rss/
COPY licenses/ /usr/share/doc/kallisti-rss/licenses/
COPY --from=build /GO-LICENSE /usr/share/doc/kallisti-rss/GO-LICENSE
WORKDIR /data
USER 65532:65532
ENTRYPOINT ["/feedbot"]
CMD ["-config", "/config/config.yaml"]
