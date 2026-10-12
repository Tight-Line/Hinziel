#!/bin/sh
# Keeps the sandbox running between the agent's commands, and starts its own
# Docker daemon if the image has one.
if [ -x /usr/bin/dockerd ]; then
	dockerd >/var/log/dockerd.log 2>&1 &
fi
exec sleep infinity
