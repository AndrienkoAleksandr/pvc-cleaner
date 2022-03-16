FROM registry.access.redhat.com/ubi8/go-toolset:1.16.12-4 as builder

WORKDIR /workspace

COPY pkg/ pkg/
COPY main.go main.go
COPY go.sum go.sum
COPY go.mod go.mod

RUN go mod download && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o pvc-cleaner main.go

FROM registry.access.redhat.com/ubi8/ubi-minimal:8.4-210

COPY --from=builder /workspace/pvc-cleaner /pvc-cleaner

ENTRYPOINT ["/pvc-cleaner"]
