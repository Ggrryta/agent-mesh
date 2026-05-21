#!/usr/bin/env bash
# install.sh — agent-meshd 一行命令安装脚本
#
# 用法:
#   curl -fsSL https://example.com/install.sh | sh
#   或本地：
#   BINARY_PATH=./dist/agent-meshd ./install.sh
#
# 流程:
#   1. 检测 OS / arch
#   2. 拼出对应的二进制名
#   3. 从 GitHub Release（或本地 BINARY_PATH）拷贝到 INSTALL_DIR
#   4. chmod +x
#   5. 提示用户下一步：
#         agent-meshd install   # 注册系统守护进程，开机自启
#         agent-meshd serve     # 前台跑（适合调试）
#         agent-meshd open      # 浏览器打开 UI
#
# 故意不在脚本里直接调 install —— 因为 install 需要环境变量
# (GATEWAY_URL / ANTHROPIC_AUTH_TOKEN)，让用户在主动 install 时再设置。

set -euo pipefail

REPO_OWNER="${REPO_OWNER:-agent-mesh}"
REPO_NAME="${REPO_NAME:-agent-mesh}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_PATH="${BINARY_PATH:-}"

echo_step() { printf "\033[1;34m==>\033[0m %s\n" "$*"; }
echo_warn() { printf "\033[1;33mwarn:\033[0m %s\n" "$*"; }
echo_err()  { printf "\033[1;31merror:\033[0m %s\n" "$*" >&2; }

# ─── 平台检测 ──────────────────────────────────────────────────────────

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$arch" in
        x86_64|amd64)  arch="x64" ;;
        arm64|aarch64) arch="arm64" ;;
        *)
            echo_err "unsupported architecture: $arch"
            exit 1
            ;;
    esac

    case "$os" in
        darwin)        echo "darwin-$arch" ;;
        linux)         echo "linux-$arch" ;;
        msys*|mingw*|cygwin*)
            if [ "$arch" != "x64" ]; then
                echo_err "windows: only x64 supported"
                exit 1
            fi
            echo "windows-x64"
            ;;
        *)
            echo_err "unsupported OS: $os"
            exit 1
            ;;
    esac
}

# ─── 检查写权限 ────────────────────────────────────────────────────────

check_install_dir() {
    if [ ! -d "$INSTALL_DIR" ]; then
        echo_err "install dir does not exist: $INSTALL_DIR"
        echo_err "set INSTALL_DIR to a writable directory and re-run"
        exit 1
    fi
    if [ ! -w "$INSTALL_DIR" ]; then
        # /usr/local/bin 在 macOS 下默认 owner 是 root；提示用户用 sudo 或换路径
        echo_warn "no write permission for $INSTALL_DIR"
        echo_warn "either:"
        echo_warn "  1. re-run with sudo:    curl ... | sudo sh"
        echo_warn "  2. set INSTALL_DIR:     INSTALL_DIR=\$HOME/.local/bin curl ... | sh"
        exit 1
    fi
}

# ─── 下载二进制 ────────────────────────────────────────────────────────

download_binary() {
    local platform="$1" target="$2"
    local binary_name="agent-meshd-$platform"
    [ "$platform" = "windows-x64" ] && binary_name="$binary_name.exe"

    if [ -n "$BINARY_PATH" ]; then
        echo_step "using local binary: $BINARY_PATH"
        if [ ! -f "$BINARY_PATH" ]; then
            echo_err "BINARY_PATH does not exist: $BINARY_PATH"
            exit 1
        fi
        cp "$BINARY_PATH" "$target"
    else
        local url
        if [ "$VERSION" = "latest" ]; then
            url="https://github.com/$REPO_OWNER/$REPO_NAME/releases/latest/download/$binary_name"
        else
            url="https://github.com/$REPO_OWNER/$REPO_NAME/releases/download/$VERSION/$binary_name"
        fi
        echo_step "downloading $url"
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL "$url" -o "$target"
        elif command -v wget >/dev/null 2>&1; then
            wget -q "$url" -O "$target"
        else
            echo_err "neither curl nor wget found"
            exit 1
        fi
    fi
    chmod +x "$target"
}

# ─── 主流程 ────────────────────────────────────────────────────────────

main() {
    echo_step "detecting platform"
    local platform
    platform="$(detect_platform)"
    echo "    platform: $platform"

    check_install_dir

    local target_name="agent-meshd"
    [ "$platform" = "windows-x64" ] && target_name="agent-meshd.exe"
    local target="$INSTALL_DIR/$target_name"

    if [ -f "$target" ]; then
        echo_warn "$target already exists, will overwrite"
    fi

    echo_step "installing to $target"
    download_binary "$platform" "$target"

    echo_step "verifying"
    "$target" version

    echo
    echo_step "next steps"
    cat <<EOF

agent-meshd is installed at:
    $target

Set the required environment, then start it in the background:

    export GATEWAY_URL=https://your-gateway.example.com
    export ANTHROPIC_AUTH_TOKEN=sk-ant-...
    # (optional)  export ANTHROPIC_BASE_URL=https://your-proxy/api
    agent-meshd start
    agent-meshd open       # opens the UI in your browser

To stop it later:

    agent-meshd stop

To run it in the foreground for debugging:

    agent-meshd run

For more, see: agent-meshd help
EOF
}

main "$@"
