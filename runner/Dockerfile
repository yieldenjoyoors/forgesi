# ForgeSI fleet agent container: a static Go binary in a scratch image.
# The agent speaks the git wire protocol itself (internal/packfile), so the
# image needs no git binary, no interpreter, no package manager.
FROM scratch

COPY agent /agent

ENV RUN_MODE=serve
ENV KNOT=https://forgesi.org
EXPOSE 8080

ENTRYPOINT ["/agent"]
