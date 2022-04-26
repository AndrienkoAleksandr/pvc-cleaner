FROM registry.access.redhat.com/ubi8/go-toolset:1.16.12-7 as builder

WORKDIR /workspace

COPY pkg pkg
COPY go.sum go.sum
COPY go.mod go.mod

USER root

RUN go mod download && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o cleaner pkg/pod_cleaner/cmd/pod_cleaner.go

FROM registry.access.redhat.com/ubi8/ubi-minimal:8.5-240

COPY --from=builder /workspace/cleaner /cleaner

# Pod cleaner should use root user to delete file with any permissionss from pvc.
USER root

ENTRYPOINT ["/cleaner"]
