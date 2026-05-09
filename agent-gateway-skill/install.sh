#!/bin/bash
# install.sh —— 安装 agent-gateway skill
#
# 用法:
#   ./install.sh [<SKILL_DIR>]
# 默认:
#   SKILL_DIR=~/.claude/skills/agent-gateway
#
# 做什么:
#   1. 拷贝本目录全部文件到 SKILL_DIR
#   2. 在 SKILL_DIR/.venv 创建 Python venv
#   3. 安装必要依赖(aiohttp, pyyaml)
#   4. 打印使用提示
#
# 为什么要自己建 venv:避免污染用户系统 Python,脚本绝对有依赖

set -euo pipefail

DEFAULT_DEST="$HOME/.claude/skills/agent-gateway"
DEST="${1:-$DEFAULT_DEST}"
SRC="$(cd "$(dirname "$0")" && pwd)"

echo "📦 安装 agent-gateway skill"
echo "   源:  $SRC"
echo "   目标: $DEST"

# 检查 Python
if ! command -v python3 >/dev/null 2>&1; then
    echo "❌ 未找到 python3。请先安装 Python 3.10+" >&2
    exit 1
fi
PY_VER=$(python3 -c 'import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")')
PY_MAJOR=$(echo "$PY_VER" | cut -d. -f1)
PY_MINOR=$(echo "$PY_VER" | cut -d. -f2)
if [ "$PY_MAJOR" -lt 3 ] || ([ "$PY_MAJOR" -eq 3 ] && [ "$PY_MINOR" -lt 10 ]); then
    echo "❌ Python 版本过低: $PY_VER。需要 3.10+" >&2
    exit 1
fi
echo "   Python $PY_VER OK"

# 检查 claude CLI
if ! command -v claude >/dev/null 2>&1; then
    echo "⚠️  未检测到 claude 命令(Agent Core 依赖它)。" >&2
    echo "    请先安装 Claude Code: https://claude.com/claude-code" >&2
    echo "    继续安装 skill 本身,但 agent 将无法上线"
fi

# 拷贝
mkdir -p "$DEST"
# 不拷贝自己和 venv
rsync -a --exclude ".venv" --exclude "install.sh" --exclude ".git" "$SRC/" "$DEST/"
cp "$SRC/install.sh" "$DEST/" 2>/dev/null || true

# 建 venv
VENV_DIR="$DEST/.venv"
if [ -d "$VENV_DIR" ]; then
    echo "   venv 已存在,跳过创建"
else
    echo "   创建 venv..."
    python3 -m venv "$VENV_DIR"
fi

# 装依赖
echo "   安装依赖..."
"$VENV_DIR/bin/pip" install --quiet --upgrade pip
"$VENV_DIR/bin/pip" install --quiet aiohttp pyyaml

chmod +x "$DEST/scripts/"*.py 2>/dev/null || true

echo ""
echo "✅ 安装完成"
echo ""
echo "下一步:"
echo "  1. 重启 Claude Code(让它扫描到新 skill)"
echo "  2. 在对话里说:"
echo "     > 初始化 Agent Gateway,地址 https://your-gateway.example.com"
echo ""
echo "skill 位置: $DEST"
