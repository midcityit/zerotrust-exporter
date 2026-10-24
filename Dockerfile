# =============================================================================
# midcityit fork of vinistoisr/zerotrust-exporter — CVE-hardened rebuild
# (MCIT-352). Upstream ships a single-stage golang:1.22-alpine image that
# keeps the full Go toolchain + build deps in the runtime layer (bloated and
# the source of ~24 CRITICAL / 563 HIGH in image scans). This multi-stage
# build compiles on current Go and ships a static binary on a distroless
# runtime, so the runtime image carries no shell, package manager, or
# toolchain CVEs.
# =============================================================================

# ── Builder ──────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
WORKDIR /app

# Module cache layer
COPY go.mod go.sum ./
RUN go mod download

# Source
COPY . .

# Bump transitive deps with known CVEs (golang.org/x/net) and re-resolve go.sum
# in-image so the build always uses patched module versions on top of the
# patched toolchain. Done here (not committed to go.mod) to keep the fork's
# source diff minimal while still shipping patched binaries.
RUN go get golang.org/x/net@v0.44.0 && go mod tidy

# Static build (CGO off -> no libc dependency in the runtime image)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /zerotrust-exporter .

# ── Runtime (distroless, non-root) ────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /zerotrust-exporter /zerotrust-exporter

EXPOSE 9184
USER nonroot:nonroot

# Distroless has no shell, so env-var expansion in the upstream CMD can't work
# here. Pass configuration via flags/env directly on the container spec; the
# program already reads API_KEY/ACCOUNT_ID/DEVICES/USERS/TUNNELS/DEX/PORT from
# the environment in init(), so no args are required.
ENTRYPOINT ["/zerotrust-exporter"]
