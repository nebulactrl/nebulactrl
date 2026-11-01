# Fixture app image. The static binary `app` is compiled by the test harness for
# the Docker daemon's architecture and placed next to this file, so building
# needs no base-image pull and no network (also used as the git-build fixture).
FROM scratch
COPY app /app
ENTRYPOINT ["/app"]
