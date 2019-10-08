FROM golang:1.13.1-alpine3.10 AS builder
RUN apk --no-cache add git upx ca-certificates
RUN mkdir -p /workspace
COPY . /workspace
WORKDIR /workspace
ENV CGO_ENABLED 0
ENV GO111MODULE on
RUN go build -ldflags "-s -w -extldflags '-static'" \
	-mod=readonly -o bin/gke-private-dns cmd/main.go
RUN upx bin/gke-private-dns

FROM scratch
COPY --from=builder /workspace/bin/gke-private-dns /gke-private-dns
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENTRYPOINT [ "/gke-private-dns" ]
