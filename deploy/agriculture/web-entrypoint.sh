#!/bin/sh
set -eu
: "${AGRI_LISTEN:=8080}"
: "${AGRI_STATIC_ROOT:=/usr/share/nginx/html}"
: "${AGRI_PLATFORM_UPSTREAM:=platform:8080}"
export AGRI_LISTEN AGRI_STATIC_ROOT AGRI_PLATFORM_UPSTREAM
# These are server deployment settings, never request/user-controlled values.
case "$AGRI_LISTEN" in ''|*[!0-9]*) echo 'Invalid listen port' >&2; exit 1;; esac
case "$AGRI_PLATFORM_UPSTREAM" in ''|*[!A-Za-z0-9.:-]*) echo 'Invalid platform upstream' >&2; exit 1;; esac
case "$AGRI_STATIC_ROOT" in /*) ;; *) echo 'Static root must be absolute' >&2; exit 1;; esac
case "$AGRI_STATIC_ROOT" in *[!A-Za-z0-9/_.-]*) echo 'Invalid static root' >&2; exit 1;; esac
envsubst '${AGRI_LISTEN} ${AGRI_STATIC_ROOT} ${AGRI_PLATFORM_UPSTREAM}' \
  < /etc/agriculture/nginx.conf.template > /tmp/agriculture-nginx.conf
nginx -t -c /tmp/agriculture-nginx.conf
exec nginx -c /tmp/agriculture-nginx.conf -g 'daemon off;'
