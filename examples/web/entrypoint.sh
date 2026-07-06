#!/bin/sh
# Proves service discovery: resolve peer names assigned via opossum's shared
# network before serving.
echo "resolving peers over the project network..."
for peer in db cache; do
  ip=$(getent hosts "$peer" | awk '{print $1}')
  echo "  $peer -> ${ip:-<unresolved>}"
done
echo "serving on :8080"
exec httpd -f -p 8080 -h /www
