# ForgeSI fleet agent container. The agent speaks the git wire protocol
# itself (internal/packfile), so the image needs no git binary — but the
# runtime base is debian-slim: the scratch variant hit opaque container
# service start failures on the Cloudflare platform (2026-11-01), while
# the same static binary runs fine on debian.
FROM debian:bookworm-slim

COPY agent /agent

ENV RUN_MODE=serve
ENV KNOT=https://forgesi.org
EXPOSE 8080

ENTRYPOINT ["/agent"]
