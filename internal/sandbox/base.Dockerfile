# Hinziel's sandbox: what every agent needs. The factory's additions follow.
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git gnupg jq less openssh-client procps unzip \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npm install -g --no-fund --no-audit @anthropic-ai/claude-code \
 && rm -rf /var/lib/apt/lists/*
# The agent has the factory's uid and gid, so it owns the run's checkouts.
ARG UID=1000
ARG GID=1000
RUN userdel --remove ubuntu 2>/dev/null; \
    getent group "$GID" >/dev/null || groupadd --gid "$GID" agent; \
    useradd --create-home --uid "$UID" --gid "$GID" --shell /bin/bash agent
