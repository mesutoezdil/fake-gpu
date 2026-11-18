FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /node-agent ./cmd/node-agent && \
    CGO_ENABLED=0 GOOS=linux go build -o /nri-plugin ./cmd/nri-plugin && \
    CGO_ENABLED=0 GOOS=linux go build -o /amd-smi ./cmd/amd-smi && \
    CGO_ENABLED=0 GOOS=linux go build -o /fakegpuctl ./cmd/fakegpuctl && \
    make -C pkg/mocksmi && make -C pkg/hipshim

FROM alpine:3.21
RUN apk add --no-cache tini
COPY --from=builder /node-agent /usr/local/bin/node-agent
COPY --from=builder /nri-plugin /usr/local/bin/nri-plugin
COPY --from=builder /amd-smi /usr/local/bin/amd-smi
COPY --from=builder /fakegpuctl /usr/local/bin/fakegpuctl
RUN ln -s amd-smi /usr/local/bin/rocm-smi
COPY --from=builder /src/pkg/mocksmi/libamd_smi.so /usr/lib64/libamd_smi.so
RUN ln -s /usr/lib64/libamd_smi.so /usr/lib64/libamd_smi.so.27
COPY --from=builder /src/pkg/hipshim/libfakehip.so /usr/lib64/libfakehip.so
COPY --from=builder /src/pkg/hipshim/fakehip.h /usr/include/fakehip.h
COPY profiles/ /etc/fake-gpu/profiles/
COPY examples/scenarios/ /usr/share/fake-gpu/scenarios/
ENTRYPOINT ["tini", "--"]
CMD ["node-agent"]
