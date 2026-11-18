#!/usr/bin/env bash
# Packrun 在线备份包装脚本：供 cron / systemd timer 调用。
# 备份内容：SQLite 一致性快照、包仓库、manifest.json（含 SHA256 与字节数）。
set -euo pipefail

BIN="${PACKRUN_BIN:-/opt/packrun/packrund}"
CONF="${PACKRUN_CONFIG:-/opt/packrun/configs/config.yaml}"
OUT="${PACKRUN_BACKUP_DIR:-/var/backups/packrun}"
KEEP="${PACKRUN_BACKUP_KEEP:-7}"

if [[ ! -x "$BIN" ]]; then
  echo "找不到可执行的 packrund: $BIN" >&2
  echo "可通过 PACKRUN_BIN 指定路径" >&2
  exit 1
fi

if [[ ! -f "$CONF" ]]; then
  echo "找不到配置文件: $CONF" >&2
  echo "可通过 PACKRUN_CONFIG 指定路径" >&2
  exit 1
fi

exec "$BIN" backup -config "$CONF" -out "$OUT" -keep "$KEEP"
