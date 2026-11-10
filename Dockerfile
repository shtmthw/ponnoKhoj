# ponnoKhoj: one statically linked binary, one image for every workload
# (serve, worker, migrate, seed). Base images are pinned by digest; the
# values below must match deploy/versions.env (`make image` passes them).
ARG GO_IMAGE=golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

# The compiler always runs natively on the build machine and cross-compiles
# for the target platform: no QEMU emulation for the compile step.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/shtmthw/ponnoKhoj/internal/version.Version=${VERSION} \
        -X github.com/shtmthw/ponnoKhoj/internal/version.Commit=${COMMIT} \
        -X github.com/shtmthw/ponnoKhoj/internal/version.Date=${DATE}" \
      -o /out/ponnokhoj ./cmd/ponnokhoj

# Distroless static, nonroot: no shell, no package manager, CA certificates
# included. Time zone data is embedded in the binary (time/tzdata).
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
LABEL org.opencontainers.image.title="ponnokhoj" \
      org.opencontainers.image.description="Find the best price of a product online, at local shops, or by evidence-based estimate." \
      org.opencontainers.image.source="https://github.com/shtmthw/ponnoKhoj" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/ponnokhoj /ponnokhoj
# 65532 is distroless's "nonroot" user and group.
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/ponnokhoj"]
CMD ["serve"]
