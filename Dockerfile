# Pensiero symbolic-reasoning gRPC server.
#
# Self-contained: pensiero links Cozo statically from lib/libcozo_c.a (via the
# #cgo directive in pkg/db/cozo.go) and links Ladybug against the liblbug.so
# vendored under vendor-libs/. The prebuilt reasoning extension (built from
# extension/reasoning/, pinned to the same Ladybug version — see
# vendor-libs/ladybug.version) is bundled at the path --reasoning-extension
# expects. No sibling checkouts, no external fetch.
#
#   docker build -t pensiero .
#   docker run --rm pensiero serve --help
#
# Published by .github/workflows/docker-publish.yml to
# ghcr.io/soundprediction/pensiero.

# ---- build stage ---------------------------------------------------------
FROM golang:1.26-bookworm AS build

# Vendored Ladybug host lib + header + prebuilt reasoning extension.
COPY vendor-libs/liblbug-linux-x86_64.so.gz /tmp/liblbug.so.gz
COPY vendor-libs/lbug.h /ladybug/lbug.h
COPY vendor-libs/libreasoning-linux-x86_64.lbug_extension.gz /tmp/libreasoning.gz
RUN mkdir -p /ladybug /ext \
    && gunzip -c /tmp/liblbug.so.gz > /ladybug/liblbug.so \
    && ln -sf liblbug.so /ladybug/liblbug.so.0 \
    && gunzip -c /tmp/libreasoning.gz > /ext/libreasoning.lbug_extension \
    && rm -f /tmp/liblbug.so.gz /tmp/libreasoning.gz

WORKDIR /src

# Dependency layer first for caching.
COPY go.mod go.sum ./
RUN go mod download

# Full source (includes lib/libcozo_c.a, which pkg/db/cozo.go links via its own
# #cgo -L${SRCDIR}/../../lib -lcozo_c).
COPY . .

# Build with CGO + system_ladybug. Point cgo at the vendored Ladybug lib/header;
# set the runtime rpath at the image lib dir. Cozo resolves via its own #cgo.
ENV CGO_ENABLED=1
RUN CGO_CFLAGS="-I/ladybug" \
    CGO_LDFLAGS="-L/ladybug -Wl,-rpath,/opt/pensiero/lib" \
    go build -tags system_ladybug -o /out/pensiero ./cmd/pensiero

# ---- runtime stage -------------------------------------------------------
# trixie (GLIBC 2.40) for parity with the predicato image.
FROM debian:trixie-slim

# liblbug.so → libstdc++/libgomp; Cozo's #cgo pulls -lz → zlib at runtime.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        libstdc++6 \
        libgomp1 \
        zlib1g \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/pensiero /usr/local/bin/pensiero
# Copy the real .so and recreate the SONAME symlink (COPY dereferences symlinks).
COPY --from=build /ladybug/liblbug.so /opt/pensiero/lib/liblbug.so
RUN ln -sf liblbug.so /opt/pensiero/lib/liblbug.so.0
COPY --from=build /ext/libreasoning.lbug_extension /opt/pensiero/libreasoning.lbug_extension

ENV LD_LIBRARY_PATH=/opt/pensiero/lib

# 50071 gRPC reasoning, 8093 health/introspection.
EXPOSE 50071 8093

ENTRYPOINT ["pensiero"]
CMD ["serve", "--help"]
