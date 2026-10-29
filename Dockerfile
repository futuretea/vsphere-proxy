# syntax=docker/dockerfile:1
FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /vsphere-proxy ./cmd/vsphere-proxy

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S proxy && adduser -S -G proxy proxy
COPY --from=build /vsphere-proxy /usr/local/bin/vsphere-proxy
COPY LICENSE THIRD_PARTY_LICENSES.md GPL-2.0.txt /usr/share/licenses/vsphere-proxy/
USER proxy
ENTRYPOINT ["/usr/local/bin/vsphere-proxy"]
CMD ["--config", "/etc/vsphere-proxy/config.json"]
