FROM alpine:3.20
# busybox-extras provides the `httpd` applet (not in Alpine's stock busybox),
# so the service stays up serving on :8080 rather than exiting immediately.
RUN apk add --no-cache busybox-extras
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && mkdir -p /www && echo "opossum web up" > /www/index.html
EXPOSE 8080
CMD ["/entrypoint.sh"]
