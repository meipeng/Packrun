#!/usr/bin/env bash
# Packrun 启动脚本
# 用法：
#   ./start.sh                 后台启动（nohup 守护，PID/日志落在 server/data/ 下）
#   ./start.sh --stop          停止后台实例
#   ./start.sh --status        查看后台实例状态
#   ./start.sh --log           跟踪运行日志
#   ./start.sh --foreground    前台启动（交给 systemd / supervisor 托管）
#   ./start.sh --print-nginx   按当前子路径打印对应的 Nginx location 配置
#   ./start.sh --help          查看可配置项
set -euo pipefail

# ======================= 启动配置（可直接修改） =======================
APP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PACKRUN_BIN="${PACKRUN_BIN:-$APP_ROOT/server/bin/packrund}"
PACKRUN_CONFIG="${PACKRUN_CONFIG:-$APP_ROOT/server/configs/config.yaml}"
# 仅在显式设置时覆盖 config.yaml 的 server.addr；未设置时沿用配置文件（默认 0.0.0.0:9000）。
# 避免改 config.yaml 的监听地址却不生效（外部访问时表现为 ERR_CONNECTION_REFUSED）。
PACKRUN_ADDR_SET="${PACKRUN_ADDR:+1}"
PACKRUN_ADDR="${PACKRUN_ADDR:-0.0.0.0:9000}"

# 部署子路径：默认 "/packrun"；如需根路径填 "/"。
# 未通过环境变量或脚本修改时，沿用 config.yaml 的 server.base_path。
PACKRUN_BASE_PATH_DEFAULT="/packrun"
PACKRUN_BASE_PATH_SET="${PACKRUN_BASE_PATH:+1}"
PACKRUN_BASE_PATH="${PACKRUN_BASE_PATH:-$PACKRUN_BASE_PATH_DEFAULT}"

# 生产默认走 SSH 真实部署；本地开发/演示可临时 PACKRUN_DEPLOY_MODE=local 覆盖。
PACKRUN_DEPLOY_MODE="${PACKRUN_DEPLOY_MODE:-ssh}"
PACKRUN_JWT_SECRET="${PACKRUN_JWT_SECRET:-}"
PACKRUN_ADMIN_PASSWORD="${PACKRUN_ADMIN_PASSWORD:-}"
PACKRUN_MAX_CONCURRENT="${PACKRUN_MAX_CONCURRENT:-}"

# Nginx 反代上游，默认跟随监听地址（0.0.0.0 / [::] 自动换成 127.0.0.1）。
default_upstream="${PACKRUN_ADDR/0.0.0.0/127.0.0.1}"
default_upstream="${default_upstream/\[::\]/127.0.0.1}"
NGINX_UPSTREAM="${NGINX_UPSTREAM:-http://$default_upstream}"

# 后台运行文件（默认后台启动时生成；工作目录固定在 server/ 下）。
RUN_DIR="$APP_ROOT/server/data"
PID_FILE="$RUN_DIR/packrund.pid"
LOG_FILE="$RUN_DIR/logs/packrund.log"
# =====================================================================

normalize_base_path() {
  local p="$1"
  if [[ -z "$p" || "$p" == "/" ]]; then
    echo "/"
    return
  fi
  [[ "$p" != /* ]] && p="/$p"
  echo "${p%/}"
}

PACKRUN_BASE_PATH="$(normalize_base_path "$PACKRUN_BASE_PATH")"

usage() {
  cat <<EOF
用法: $0 [--daemon | --foreground | --stop | --status | --log | --print-nginx] [--help]

运行方式：
  --daemon        后台启动（默认行为；nohup 守护，重启后需手动拉起）
  --foreground    前台启动（交给 systemd / supervisor 托管时用）
  --stop          停止后台实例
  --status        查看后台实例状态
  --log           跟踪运行日志（tail -f）

可在脚本顶部直接修改，或用同名环境变量覆盖：
  PACKRUN_BIN            二进制路径（当前: ${PACKRUN_BIN}）
  PACKRUN_CONFIG         配置文件路径（当前: ${PACKRUN_CONFIG}）
  PACKRUN_ADDR           监听地址（当前: ${PACKRUN_ADDR}；不设置则用 config.yaml 的 server.addr）
  PACKRUN_BASE_PATH      部署子路径（当前: ${PACKRUN_BASE_PATH}，默认 /packrun）
  PACKRUN_DEPLOY_MODE    local | ssh（当前: ${PACKRUN_DEPLOY_MODE}）
  PACKRUN_JWT_SECRET     JWT 密钥（release 模式必填，≥32 字符）
  PACKRUN_ADMIN_PASSWORD 首次初始化管理员口令（release 模式必填）
  PACKRUN_MAX_CONCURRENT 全局并发上限
  NGINX_UPSTREAM         Nginx 反代上游（当前: ${NGINX_UPSTREAM}）

  --print-nginx  打印与 PACKRUN_BASE_PATH 匹配的 Nginx location 配置
EOF
}

print_nginx() {
  local base="$PACKRUN_BASE_PATH"
  local upstream="$NGINX_UPSTREAM"

  if [[ "$base" == "/" ]]; then
    cat <<EOF
# Packrun 根路径反代（PACKRUN_BASE_PATH=/）
location / {
    proxy_pass ${upstream};
    proxy_http_version 1.1;
    proxy_set_header Host              \$host;
    proxy_set_header X-Real-IP         \$remote_addr;
    proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;
    client_max_body_size 200m;
    proxy_request_buffering off;
    proxy_buffering         off;
    proxy_connect_timeout   10s;
    proxy_send_timeout      600s;
    proxy_read_timeout      600s;
}
EOF
    return
  fi

  cat <<EOF
# Packrun 子路径反代（PACKRUN_BASE_PATH=${base}）
# 关键：proxy_pass 后不要带 URI/尾斜杠，保留 ${base}/ 前缀交给应用处理。
# 写成 ${upstream}/ 会剥离前缀，与应用内子路径配置不一致，导致跳转循环或 404。
location = ${base} {
    return 301 ${base}/\$is_args\$args;
}

location ${base}/ {
    proxy_pass ${upstream};
    proxy_http_version 1.1;
    proxy_set_header Host              \$host;
    proxy_set_header X-Real-IP         \$remote_addr;
    proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;
    client_max_body_size 200m;
    proxy_request_buffering off;
    proxy_buffering         off;
    proxy_connect_timeout   10s;
    proxy_send_timeout      600s;
    proxy_read_timeout      600s;
}
EOF
}

# ---------- 后台运行管理 ----------

# pid_alive 判断 PID 文件指向的进程是否存活；文件缺失或内容非法均视为不存活。
pid_alive() {
  local pid
  pid="$(cat "$PID_FILE" 2>/dev/null)" || return 1
  kill -0 "$pid" 2>/dev/null
}

start_daemon() {
  mkdir -p "$RUN_DIR/logs"
  if pid_alive; then
    echo "已在后台运行（PID $(cat "$PID_FILE")）；如需重启请先执行 $0 --stop" >&2
    exit 1
  fi
  rm -f "$PID_FILE"
  nohup "$PACKRUN_BIN" -config "$PACKRUN_CONFIG" >> "$LOG_FILE" 2>&1 &
  local pid=$!
  echo "$pid" > "$PID_FILE"
  sleep 1
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "启动失败，最近日志如下（完整日志: ${LOG_FILE}）：" >&2
    tail -n 20 "$LOG_FILE" >&2
    rm -f "$PID_FILE"
    exit 1
  fi
  echo "后台启动成功（PID ${pid}）"
  echo "  日志:   $0 --log   （或 tail -f ${LOG_FILE}）"
  echo "  状态:   $0 --status"
  echo "  停止:   $0 --stop"
}

stop_daemon() {
  if ! pid_alive; then
    echo "未在后台运行（或 PID 文件已过期）"
    rm -f "$PID_FILE"
    exit 0
  fi
  local pid
  pid="$(cat "$PID_FILE")"
  echo "停止后台实例（PID ${pid}）..."
  kill "$pid"
  for _ in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "优雅停止超时，发送 SIGKILL"
    kill -9 "$pid"
  fi
  rm -f "$PID_FILE"
  echo "已停止（PID ${pid}）"
}

status_daemon() {
  if pid_alive; then
    echo "运行中（PID $(cat "$PID_FILE")）"
    echo "  日志: $LOG_FILE"
  else
    echo "未在后台运行"
    rm -f "$PID_FILE"
  fi
}

tail_log() {
  if [[ ! -f "$LOG_FILE" ]]; then
    echo "暂无日志文件: $LOG_FILE"
    exit 0
  fi
  exec tail -n 200 -f "$LOG_FILE"
}

run_mode="daemon"
case "${1:-}" in
  --print-nginx)
    print_nginx
    exit 0
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  --foreground | -f)
    run_mode="foreground"
    ;;
  -d | --daemon | "")
    run_mode="daemon"
    ;;
  --stop)
    stop_daemon
    exit 0
    ;;
  --status)
    status_daemon
    exit 0
    ;;
  --log)
    tail_log
    exit 0
    ;;
  *)
    echo "未知参数: $1" >&2
    usage >&2
    exit 2
    ;;
esac

if [[ ! -x "$PACKRUN_BIN" ]]; then
  echo "未找到可执行文件: $PACKRUN_BIN" >&2
  echo "请先在项目根目录执行 make build" >&2
  exit 1
fi

if [[ -n "$PACKRUN_ADDR_SET" ]]; then
  export PACKRUN_ADDR
fi
export PACKRUN_DEPLOY_MODE
# 仅在环境变量显式设置或脚本中改为非默认值时导出，否则交给 config.yaml 的 server.base_path。
if [[ -n "$PACKRUN_BASE_PATH_SET" || "$PACKRUN_BASE_PATH" != "$PACKRUN_BASE_PATH_DEFAULT" ]]; then
  export PACKRUN_BASE_PATH
fi
if [[ -n "$PACKRUN_JWT_SECRET" ]]; then
  export PACKRUN_JWT_SECRET
fi
if [[ -n "$PACKRUN_ADMIN_PASSWORD" ]]; then
  export PACKRUN_ADMIN_PASSWORD
fi
if [[ -n "$PACKRUN_MAX_CONCURRENT" ]]; then
  export PACKRUN_MAX_CONCURRENT
fi

# 固定工作目录，保证配置中的 ./data/... 相对路径落在 server/ 下。
cd "$APP_ROOT/server"

echo "Packrun 启动（${run_mode}）"
echo "  二进制:   $PACKRUN_BIN"
echo "  配置:     $PACKRUN_CONFIG"
if [[ -n "$PACKRUN_ADDR_SET" ]]; then
  echo "  监听:     $PACKRUN_ADDR（环境变量）"
else
  echo "  监听:     config.yaml 的 server.addr（默认 $PACKRUN_ADDR）"
fi
echo "  部署路径: $PACKRUN_BASE_PATH"
echo "  本机访问: http://127.0.0.1:${PACKRUN_ADDR##*:}${PACKRUN_BASE_PATH%/}/"
echo "  Nginx:    $0 --print-nginx"

if [[ "$run_mode" == "daemon" ]]; then
  start_daemon
else
  exec "$PACKRUN_BIN" -config "$PACKRUN_CONFIG"
fi
