# Read-only git daemon serving /srv/git (the harness COPYs a bare `app.git` repo
# next to this file into the build context). Anonymous clone: git://<host>/app.git
FROM alpine:3
RUN apk add --no-cache git git-daemon \
 && git config --system safe.directory '*'
COPY app.git /srv/git/app.git
EXPOSE 9418
ENTRYPOINT ["git", "daemon", "--reuseaddr", "--export-all", "--base-path=/srv/git", "--listen=0.0.0.0", "--port=9418", "/srv/git"]
