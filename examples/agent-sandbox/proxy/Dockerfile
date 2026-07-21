# A tiny default-deny forward proxy: the caged agent's single, enforced exit.
# It sits on the default (internet-facing) network and republishes port 8888 to
# the host; the caged agent — which has no internet of its own — reaches it via
# ${OPOSSUM_HOST_GATEWAY}. tinyproxy filters by destination host, so the allowlist
# (bind-mounted, see compose.yaml) is what the agent is permitted to reach.
FROM alpine:3.20
RUN apk add --no-cache tinyproxy
COPY tinyproxy.conf /etc/tinyproxy/tinyproxy.conf
EXPOSE 8888
# -d keeps tinyproxy in the foreground so the container's lifecycle tracks it.
ENTRYPOINT ["tinyproxy", "-d", "-c", "/etc/tinyproxy/tinyproxy.conf"]
