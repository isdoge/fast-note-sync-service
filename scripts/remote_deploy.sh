#!/usr/bin/env bash
set -euo pipefail

# ===========================================
# Fast-Note Sync Service 远程部署脚本
# 在本地 (macOS/Linux) 运行，通过 SSH 把服务端部署到远程 Linux 服务器。
# 用法: remote_deploy.sh <host> [选项]
# 详细文档: docs/runbook/remote-deploy.zh-CN.md
# ===========================================

REPO_SCRIPT="scripts/quest_install.sh"
INSTALL_DIR="/opt/fast-note"
HEALTH_PATH="/api/health"
IMAGE="ghcr.io/isdoge/fast-note-sync-service"
COMPOSE_DIR="/opt/fast-note-docker"
SSH_OPTS=(-o BatchMode=no -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)

# --- 参数 ---
HOST=""
SSH_USER="root"
SSH_PORT="22"
SSH_KEY=""
VERSION="latest"
HTTP_PORT=""
DEPLOY_MODE="binary"   # binary | docker
TARBALL=""
FORCE=0
SSHPASS_MODE=0

usage() {
    cat <<'EOF'
用法: remote_deploy.sh <host> [选项]

通过 SSH 把 Fast-Note Sync Service 部署到远程 Linux 服务器。

参数:
  <host>                      服务器地址 (必填)
  -u, --user <user>           SSH 用户 (默认 root)
  -p, --port <port>           SSH 端口 (默认 22)
  -i, --key <path>            SSH 私钥路径
  -v, --version <ver>         要安装的版本 (默认 latest)
  --http-port <port>          服务端 HTTP 端口 (默认沿用服务端配置 :9000)
  --mode <binary|docker>      部署方式 (默认 binary)
  --tarball <path>            本地 tar.gz 安装包，离线安装未发布版本
  --force                     允许破坏性操作 (目前用于跳过已安装提示直接升级)
  -h, --help                  显示帮助

认证:
  优先使用密钥 (-i 或默认 ssh 配置)。无密钥时 ssh 会提示输入密码，
  本脚本使用 ControlMaster 连接复用，整个流程只需输入一次。
  密码不会出现在命令行、进程列表或任何文件中。
  如本机装有 sshpass 且设置了环境变量 SSHPASS，则以非交互方式认证。

示例:
  remote_deploy.sh 203.0.113.10
  remote_deploy.sh example.com -u ubuntu -i ~/.ssh/id_ed25519 --mode docker
  remote_deploy.sh 192.168.1.5 --tarball ./fast-note-sync-service-3.7.0-linux-amd64.tar.gz
EOF
}

die() { echo "[-] $*" >&2; exit 1; }
step() { echo ">> $*"; }
ok() { echo "[+] $*"; }
warn() { echo "[!] $*"; }

# --- 解析参数 ---
while [ "$#" -gt 0 ]; do
    case "$1" in
        -u|--user) SSH_USER="${2:?}"; shift 2;;
        -p|--port) SSH_PORT="${2:?}"; shift 2;;
        -i|--key) SSH_KEY="${2:?}"; shift 2;;
        -v|--version) VERSION="${2:?}"; shift 2;;
        --http-port) HTTP_PORT="${2:?}"; shift 2;;
        --mode) DEPLOY_MODE="${2:?}"; shift 2;;
        --tarball) TARBALL="${2:?}"; shift 2;;
        --force) FORCE=1; shift;;
        -h|--help) usage; exit 0;;
        -*) die "未知参数: $1 (见 --help)";;
        *)
            if [ -n "$HOST" ]; then die "只能指定一个服务器地址: $HOST 与 $1"; fi
            HOST="$1"; shift;;
    esac
done

[ -n "$HOST" ] || { usage >&2; die "缺少服务器地址"; }
case "$DEPLOY_MODE" in
    binary|docker) ;;
    *) die "--mode 只能是 binary 或 docker";;
esac
if [ -n "$TARBALL" ] && [ ! -f "$TARBALL" ]; then
    die "本地安装包不存在: $TARBALL"
fi
[ "$DEPLOY_MODE" = "docker" ] && [ -n "$TARBALL" ] && die "--tarball 只用于 binary 模式（docker 模式用镜像）"

# 在建立连接前校验，避免 SSH/SCP 选项注入及 compose 内容注入。
validate_port() {
    [[ "$2" =~ ^[0-9]{1,5}$ ]] && (( 10#$2 >= 1 && 10#$2 <= 65535 )) \
        || die "$1 端口必须为 1–65535 的数字"
}
validate_port SSH "$SSH_PORT"
SSH_PORT="$((10#$SSH_PORT))"
if [ -n "$HTTP_PORT" ]; then
    validate_port HTTP "$HTTP_PORT"
    HTTP_PORT="$((10#$HTTP_PORT))"
fi
[[ "$HOST" =~ ^[a-zA-Z0-9][a-zA-Z0-9.:%_-]*$ ]] || die "服务器地址含不支持的字符"
[[ "$SSH_USER" =~ ^[a-zA-Z_][a-zA-Z0-9_-]*$ ]] || die "SSH 用户名含不支持的字符"
[[ "$VERSION" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$ ]] || die "版本号含不支持的字符"

# printf %q 生成的 Bash 参数在明确的 Bash 中解析，外层只用 POSIX 单引号。
run_remote() {
    local command
    printf -v command '%q ' "$@"
    command=${command//\'/\'\"\'\"\'}
    run_ssh "bash -c '$command'"
}

# --- 本地安装脚本路径 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALLER="$SCRIPT_DIR/quest_install.sh"
[ -f "$INSTALLER" ] || die "找不到 quest_install.sh: $INSTALLER"

# --- SSH 认证与连接复用 ---
if [ -n "$SSH_KEY" ]; then
    [ -f "$SSH_KEY" ] || die "私钥不存在: $SSH_KEY"
    SSH_OPTS+=(-i "$SSH_KEY")
fi

# ControlMaster socket 路径必须足够短 (macOS Unix domain socket 上限约 104 字节)
SSH_CTRL="/tmp/fns-rd-%r@%h:%p.$$"
REMOTE_TMP=""
PROBE_LOCAL=""
cleanup() {
    if [ -n "$REMOTE_TMP" ]; then
        run_remote find "$REMOTE_TMP" -depth -delete >/dev/null 2>&1 || true
    fi
    [ -z "$PROBE_LOCAL" ] || rm -f -- "$PROBE_LOCAL"
    ssh -o ControlPath="$SSH_CTRL" -O exit -p "$SSH_PORT" "$SSH_USER@$HOST" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

SSH_BASE=(ssh "${SSH_OPTS[@]}" -o ControlMaster=auto -o ControlPersist=300 -o ControlPath="$SSH_CTRL" -p "$SSH_PORT" "$SSH_USER@$HOST")
SCP_BASE=(scp "${SSH_OPTS[@]}" -o ControlPath="$SSH_CTRL" -P "$SSH_PORT")

# sshpass 非交互模式：仅当本机有 sshpass 且用户提供了 SSHPASS 环境变量
if [ -n "${SSHPASS:-}" ]; then
    command -v sshpass >/dev/null 2>&1 || die "设置了 SSHPASS 但本机没有安装 sshpass"
    SSHPASS_MODE=1
fi
if [ "$SSHPASS_MODE" -eq 1 ]; then
    # 触发一次主连接，后续复用
    SSHPASS="$SSHPASS" sshpass -e ssh "${SSH_OPTS[@]}" -o ControlMaster=yes -o ControlPersist=300 -o ControlPath="$SSH_CTRL" -p "$SSH_PORT" "$SSH_USER@$HOST" true >/dev/null 2>&1 || die "SSH 连接失败 (检查地址、端口、用户名或密码)"
    run_ssh() { "${SSH_BASE[@]}" "$@"; }
    run_scp() { "${SCP_BASE[@]}" "$@"; }
else
    # 交互模式：首次连接由 ssh 提示输密码，之后 ControlMaster 复用，只输一次
    "${SSH_BASE[@]}" true >/dev/null 2>&1 || die "SSH 连接失败 (检查地址、端口、用户名、密码或密钥)"
    run_ssh() { "${SSH_BASE[@]}" "$@"; }
    run_scp() { "${SCP_BASE[@]}" "$@"; }
fi

ok "SSH 连接成功: $SSH_USER@$HOST:$SSH_PORT"

# --- 远端环境检查 ---
REMOTE_TMP="$(run_ssh "mktemp -d /tmp/fns-rd.XXXXXX")" || die "远端创建临时目录失败"
[[ "$REMOTE_TMP" =~ ^/tmp/fns-rd\.[a-zA-Z0-9]{6}$ ]] || { REMOTE_TMP=""; die "远端临时目录格式异常"; }
probe_remote() {
    run_scp -q "$PROBE_LOCAL" "$SSH_USER@$HOST:$REMOTE_TMP/fns-probe.sh" || return 1
    run_remote bash "$REMOTE_TMP/fns-probe.sh"
}
PROBE_LOCAL="$(mktemp)"
cat > "$PROBE_LOCAL" <<'PROBE'
arch="$(uname -m)"
kernel="$(uname -s)"
os="$(grep '^ID=' /etc/os-release 2>/dev/null | cut -d= -f2 || echo unknown)"
uid="$(id -u)"
if command -v sudo >/dev/null 2>&1; then hassudo=sudo; else hassudo=none; fi
if command -v systemctl >/dev/null 2>&1; then hassys=systemd; else hassys=none; fi
echo "$kernel|$arch|$os|$uid|$hassudo|$hassys"
PROBE
REMOTE_INFO="$(probe_remote)" || { rm -f "$PROBE_LOCAL"; die "无法读取远端系统信息"; }
rm -f "$PROBE_LOCAL"
IFS='|' read -r REMOTE_KERNEL REMOTE_ARCH REMOTE_OS REMOTE_UID HAS_SUDO HAS_SYSTEMD <<< "$REMOTE_INFO"
ok "远端系统: ${REMOTE_OS:-unknown}/$REMOTE_KERNEL ($REMOTE_ARCH), systemd: $HAS_SYSTEMD"

[ "$REMOTE_KERNEL" = "Linux" ] || die "远端不是 Linux 系统，本脚本仅支持 Linux 服务器"
SUDO_CMD=""
if [ "$REMOTE_UID" != "0" ]; then
    [ "$HAS_SUDO" = "sudo" ] || die "远端用户 $SSH_USER 不是 root 且没有 sudo，无法安装"
    # sudo -n 探测是否需要密码；需要密码时给 ssh 加 -tt 让 sudo 能交互提示
    if run_ssh "sudo -n true >/dev/null 2>&1"; then
        warn "远端用户 $SSH_USER 不是 root，将通过免密 sudo 执行"
    elif [ -t 0 ]; then
        warn "远端用户 $SSH_USER 不是 root，sudo 需要密码，将在执行时提示输入"
        SSH_BASE+=(-tt)
    else
        die "远端用户 $SSH_USER 不是 root 且 sudo 需要密码，而当前环境无法交互输入；请改用 root 登录或配置 sudo 免密 (NOPASSWD)"
    fi
    SUDO_CMD="sudo"
fi

# 已安装检测
ALREADY=0
if run_ssh "[ -x $INSTALL_DIR/fast-note-sync-service ]"; then
    ALREADY=1
    REMOTE_VER="(未知)"
    REMOTE_VER="$(run_ssh "cat $INSTALL_DIR/.version 2>/dev/null || cat $INSTALL_DIR/config/lastVersion 2>/dev/null || true")"
    if [ "$FORCE" -eq 0 ] && [ "$DEPLOY_MODE" = "docker" ]; then
        warn "远端已存在二进制安装 (v${REMOTE_VER:-?})。切换到 docker 模式前请确认是否卸载；如确要继续请加 --force"
        die "已存在的安装与 docker 模式冲突"
    fi
    if [ "$FORCE" -eq 1 ]; then
        warn "远端已安装 v${REMOTE_VER:-?}，执行升级（保留 config/ 与数据）"
    else
        ok "远端已安装 v${REMOTE_VER:-?}，执行升级（保留 config/ 与数据）"
    fi
fi

# 切换前停止另一种部署，保留其程序、配置及数据供手工恢复。
DOCKER_EXISTS=0
if run_ssh "command -v docker >/dev/null 2>&1"; then
    run_ssh "$SUDO_CMD docker info >/dev/null 2>&1" || die "无法检查 Docker 旧实例，请检查 Docker 权限或守护进程"
    if run_ssh "$SUDO_CMD docker inspect --format '{{.Name}}' fast-note-sync-service >/dev/null 2>&1"; then
        DOCKER_EXISTS=1
    fi
fi
if [ "$DEPLOY_MODE" = "binary" ] && [ "$DOCKER_EXISTS" -eq 1 ]; then
    [ "$FORCE" -eq 1 ] || die "已存在 Docker 容器；切换到 binary 模式需 --force"
    step "停止旧 Docker 实例并关闭其自动重启..."
    run_ssh "$SUDO_CMD docker update --restart=no fast-note-sync-service && $SUDO_CMD docker stop fast-note-sync-service" \
        || die "停止旧 Docker 实例失败，已中止；请检查容器状态"
    RUNNING="$(run_ssh "$SUDO_CMD docker inspect --format '{{.State.Running}}' fast-note-sync-service")" \
        || die "无法确认旧 Docker 实例已停止，已中止"
    [ "$RUNNING" = "false" ] || die "旧 Docker 实例仍在运行，已中止"
fi

stop_binary() {
    # 同时处理 systemd 和脱离 systemd 的 nohup 实例，停不下来则不启动容器。
    run_remote ${SUDO_CMD:+"$SUDO_CMD"} bash -c '
set -e
if command -v systemctl >/dev/null 2>&1 && systemctl cat fast-note.service >/dev/null 2>&1; then
    systemctl disable --now fast-note.service
    if systemctl is-active --quiet fast-note.service; then exit 1; fi
fi
pattern="^([^[:space:]]*/)?fast-note-sync-service[[:space:]]+run([[:space:]]|$)"
if pgrep -f "$pattern" >/dev/null; then pkill -TERM -f "$pattern"; fi
for ((i=0; i<10; i++)); do
    if pgrep -f "$pattern" >/dev/null; then sleep 1; else
        rc=$?
        [ "$rc" -eq 1 ] && exit 0
        exit "$rc"
    fi
done
exit 1
'
}

# --- 部署 ---
if [ "$DEPLOY_MODE" = "binary" ]; then
    step "部署方式: 二进制 + systemd"
    if [ "$HAS_SYSTEMD" != "systemd" ] && [ "$FORCE" -eq 0 ]; then
        warn "远端没有 systemd，服务将以后台进程方式启动（重启机器后不会自启）"
    fi
    if [ "$HAS_SYSTEMD" != "systemd" ] && [ "$FORCE" -eq 1 ]; then
        ok "远端没有 systemd，服务将以后台进程方式启动（重启机器后不会自启）"
    fi

    step "上传安装脚本与安装包..."
    run_scp -q "$INSTALLER" "$SSH_USER@$HOST:$REMOTE_TMP/quest_install.sh" || die "上传 quest_install.sh 失败"

    INSTALL_ARGS=(install "$VERSION" --yes)
    if [ -n "$TARBALL" ]; then
        # 上传到固定目录，SCP 的远端参数不含用户包名（兼容旧 SCP 协议）。
        TARBALL="$(cd "$(dirname "$TARBALL")" && pwd)/$(basename "$TARBALL")"
        run_scp -q "$TARBALL" "$SSH_USER@$HOST:$REMOTE_TMP/" || die "上传安装包失败"
        INSTALL_ARGS+=(--tarball "$REMOTE_TMP/$(basename "$TARBALL")")
    fi

    # 安装器自身负责自启注册，不再以普通用户重复调用。
    step "远端执行非交互安装..."
    run_remote ${SUDO_CMD:+"$SUDO_CMD"} bash "$REMOTE_TMP/quest_install.sh" "${INSTALL_ARGS[@]}" \
        || die "远端安装失败（已完成的步骤: 环境检查、上传）"

else
    step "部署方式: Docker Compose"
    run_ssh "command -v docker >/dev/null 2>&1" || die "远端没有安装 docker（请先安装 docker，本脚本不会擅自安装）"
    run_ssh "docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1" || die "远端没有 docker compose 插件"

    IMAGE_REF="$IMAGE"
    [ "$VERSION" != "latest" ] && IMAGE_REF="$IMAGE:${VERSION#v}"

    PORT_MAP="9000:9000"
    [ -n "$HTTP_PORT" ] && PORT_MAP="$HTTP_PORT:9000"

    stop_binary || die "停止旧二进制实例失败，已中止；请检查 systemd/nohup 进程状态"
    step "写入 compose 文件并拉取镜像..."
    run_ssh "$SUDO_CMD mkdir -p $COMPOSE_DIR" || die "创建 $COMPOSE_DIR 失败"
    run_ssh "$SUDO_CMD tee $COMPOSE_DIR/docker-compose.yaml >/dev/null" <<EOF
services:
  fast-note-sync-service:
    image: $IMAGE_REF
    container_name: fast-note-sync-service
    restart: unless-stopped
    ports:
      - "$PORT_MAP"
    volumes:
      - $INSTALL_DIR/storage/:/fast-note-sync/storage/
      - $INSTALL_DIR/config/:/fast-note-sync/config/
EOF
    run_ssh "cd $COMPOSE_DIR && $SUDO_CMD \$(command -v docker >/dev/null 2>&1 && (docker compose version >/dev/null 2>&1 && echo 'docker compose up -d --pull always' || echo 'docker-compose up -d'))" \
        || die "docker compose 启动失败（已完成的步骤: 环境检查、compose 文件写入）"
    run_ssh "$SUDO_CMD docker update --restart=unless-stopped fast-note-sync-service >/dev/null" \
        || die "恢复 Docker 自动重启策略失败"
fi

# --- 可选: 修改 HTTP 端口 (binary 模式) ---
if [ "$DEPLOY_MODE" = "binary" ] && [ -n "$HTTP_PORT" ]; then
    step "设置 HTTP 端口为 :$HTTP_PORT"
    run_remote ${SUDO_CMD:+"$SUDO_CMD"} sed -i "s|^\\([[:space:]]*http-port:\\).*$|\\1 \":$HTTP_PORT\"|" "$INSTALL_DIR/config/config.yaml" \
        || die "修改 http-port 失败"
    if [ "$HAS_SYSTEMD" = "systemd" ]; then
        run_ssh "$SUDO_CMD systemctl restart fast-note.service || true"
    else
        # set -m + 子 shell + </dev/null：脱离会话进程组避免 ssh 断开时被 SIGHUP，且不继承 ssh 通道
        # pkill 用 ^ 锚定，防止匹配到携带同样路径的 ssh 会话命令行而误杀自身
        run_ssh "$SUDO_CMD pkill -f '^$INSTALL_DIR/fast-note-sync-service run' || true; sleep 1; $SUDO_CMD bash -c 'set -m; cd $INSTALL_DIR && (nohup ./fast-note-sync-service run </dev/null >> /var/log/fast-note.log 2>&1 &)'" || true
    fi
fi

# --- 健康检查 ---
HEALTH_PORT="${HTTP_PORT:-9000}"
step "等待健康检查 http://$HOST:$HEALTH_PORT$HEALTH_PATH ..."
HEALTH_OK=0
for i in $(seq 1 30); do
    if HEALTH_BODY="$(run_remote curl -fsS -m 3 "http://127.0.0.1:$HEALTH_PORT$HEALTH_PATH" 2>/dev/null)"; then
        HEALTH_OK=1
        break
    fi
    sleep 2
done
if [ "$HEALTH_OK" -ne 1 ]; then
    run_ssh "$SUDO_CMD tail -n 20 /var/log/fast-note.log 2>/dev/null || $SUDO_CMD journalctl -u fast-note.service -n 20 --no-pager 2>/dev/null || true" || true
    die "健康检查 30 次未通过，部署未完全成功（安装已完成，请检查远端日志）"
fi
if [ "$DEPLOY_MODE" = "binary" ] && [ "$VERSION" != "latest" ]; then
    ACTUAL_VERSION="$(printf '%s' "$HEALTH_BODY" | sed -nE 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p')"
    [ "${ACTUAL_VERSION#v}" = "${VERSION#v}" ] \
        || die "运行版本不匹配：请求 ${VERSION}，实际 ${ACTUAL_VERSION:-未知}"
fi
ok "健康检查通过，服务已就绪"
ok "访问地址: http://$HOST:$HEALTH_PORT"

# --- 收尾提示 ---

cat <<EOF

[+] 部署完成。接下来建议:
    1. 首次访问 http://$HOST:$HEALTH_PORT 注册管理员账号
    2. 注册完成后在 $INSTALL_DIR/config/config.yaml 中关闭开放注册:
       register-is-enable: false
    3. 在服务器防火墙/安全组放行 TCP $HEALTH_PORT
    4. 生产环境建议配置反向代理并启用 HTTPS (参考 docs/runbook/)
EOF
