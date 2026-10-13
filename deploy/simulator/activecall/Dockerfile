# syntax=docker/dockerfile:1
# New isolated build only. Never patch or replace an existing Active Call image.
FROM rust:1.94.0-bookworm@sha256:365468470075493dc4583f47387001854321c5a8583ea9604b297e67f01c5a4f AS source
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates clang cmake git libclang-dev libssl-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git init . && git remote add origin https://github.com/miuda-ai/active-call.git \
    && git fetch --depth=1 origin eaf7ebc8f39193a289348989f6a09b0ace287da7 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = eaf7ebc8f39193a289348989f6a09b0ace287da7
COPY handshake.patch /tmp/handshake.patch
RUN git apply --check /tmp/handshake.patch && git apply /tmp/handshake.patch

# Upstream excludes Cargo.lock. This explicit maintenance target produces the
# committed simulator lock; normal builds never resolve floating dependencies.
FROM source AS lockgen
RUN cargo generate-lockfile
FROM scratch AS lock-export
COPY --from=lockgen /src/Cargo.lock /Cargo.lock

FROM source AS build
COPY Cargo.lock /src/Cargo.lock
RUN cargo build --release --locked --no-default-features --bin active-call --jobs 2

FROM debian:12.11-slim@sha256:b1a741487078b369e78119849663d7f1a5341ef2768798f7b7406c4240f86aef
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --create-home simulator
COPY --from=build /src/target/release/active-call /usr/local/bin/active-call
WORKDIR /tmp
USER 65532
LABEL org.opencontainers.image.source="https://github.com/miuda-ai/active-call" \
      org.opencontainers.image.revision="eaf7ebc8f39193a289348989f6a09b0ace287da7" \
      io.golive.simulator.patch="custom-realtime-websocket-handshake-v1"
ENTRYPOINT ["/usr/local/bin/active-call"]
CMD ["--conf", "/run/simulator/active-call.toml"]
