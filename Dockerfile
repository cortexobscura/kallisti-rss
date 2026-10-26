FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN cp /usr/local/go/LICENSE /GO-LICENSE
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /feedbot ./cmd/kallisti-rss
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /feedbot /feedbot
COPY LICENSE THIRD_PARTY.md /usr/share/doc/kallisti-rss/
COPY licenses/ /usr/share/doc/kallisti-rss/licenses/
COPY --from=build /GO-LICENSE /usr/share/doc/kallisti-rss/GO-LICENSE
WORKDIR /data
USER 65532:65532
ENTRYPOINT ["/feedbot"]
CMD ["-config", "/config/config.yaml"]
