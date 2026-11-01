# One static binary in a minimal image, run as the torrent user with a
# read-only filesystem (S6, N4). The release workflow builds it from a version
# tag and publishes it to ghcr.io/kabsdk/peerglass; ../ansible-infrastructure
# pins the version and digest.
#
# Both base images are pinned by digest (S7): the tag says what they are,
# the digest makes sure that is what we get.
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /peerglass ./cmd/peerglass

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# Links the published package to its repository, so the repository's
# permissions apply to it
LABEL org.opencontainers.image.source="https://github.com/kabsdk/peerglass" \
      org.opencontainers.image.description="A small, security-minded web UI for rTorrent"
COPY --from=build /peerglass /peerglass
USER 1004:1004
EXPOSE 8080
ENTRYPOINT ["/peerglass"]
