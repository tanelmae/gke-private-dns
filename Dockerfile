FROM golang:1.13.1-alpine3.10 AS builder
RUN apk --no-cache add git upx ca-certificates
RUN mkdir -p /workspace

# This will allow caching dependencies and not triggering
# fetching dependencies for every code change
COPY go.* /workspace/
WORKDIR /workspace
RUN go mod download

COPY . /workspace
ENV CGO_ENABLED 0
RUN go build -ldflags "-s -w -extldflags '-static'" \
	-mod=readonly -o bin/private-dns cmd/main.go
RUN upx bin/private-dns

FROM scratch
COPY --from=builder /workspace/bin/private-dns /bin/private-dns
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENTRYPOINT [ "/bin/private-dns" ]
