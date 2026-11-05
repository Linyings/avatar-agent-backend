#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
printf 'Acquire::ForceIPv4 "true";\nAcquire::Languages "none";\n' >/etc/apt/apt.conf.d/99-aicat-network
if [[ ! -f /etc/apt/sources.list.d/ubuntu.sources.aicat-original ]]; then
  cp /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list.d/ubuntu.sources.aicat-original
  sed -i 's|http://archive.ubuntu.com/ubuntu/|https://mirrors.aliyun.com/ubuntu/|g; s|http://security.ubuntu.com/ubuntu/|https://mirrors.aliyun.com/ubuntu/|g' /etc/apt/sources.list.d/ubuntu.sources
fi
apt-get update
apt-get install -y ca-certificates curl gnupg rsync python3
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -4 -fsSL --retry 3 --connect-timeout 15 https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n' "$(dpkg --print-architecture)" >/etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
if [[ ! -f /etc/docker/daemon.json ]]; then
  install -d /etc/docker
  printf '{"registry-mirrors":["https://docker.m.daocloud.io"],"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}\n' >/etc/docker/daemon.json
fi
systemctl enable --now docker
systemctl restart docker
docker version --format '{{.Server.Version}}'
docker compose version
