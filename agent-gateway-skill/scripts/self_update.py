#!/usr/bin/env python3
"""
self_update.py —— 从 Gateway 拉最新 skill tarball 完成原子升级

流程:
  1. 读本地 VERSION
  2. GET {gateway}/skill/version 拿远端版本 + sha256
  3. 相同则 "已是最新",退出 0
  4. 不同:下载 -> sha256 校验 -> 停 daemon -> 原子替换 -> 重启 daemon -> 健康检查
  5. 启动失败自动回滚(把 .old 换回来,再启 daemon)

使用:
  python3 self_update.py             # 检查 + 升级(需确认)
  python3 self_update.py --check     # 只检查,不升级
  python3 self_update.py --force     # 强制升级(本地/远端版本相同也升)
  python3 self_update.py --yes       # 非交互,默认同意

脚本在 skill 自己的 venv 下运行(和其他 script 一样)。
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

from _common import (
    HOME_CONFIG_DIR,
    SKILL_ROOT,
    die,
    info,
    load_skill_config,
    ok,
)


def _local_version() -> str:
    vf = SKILL_ROOT / "VERSION"
    if not vf.exists():
        return "unknown"
    return vf.read_text().strip()


def _remote_version(gateway_url: str, timeout: float = 5.0) -> dict:
    url = gateway_url.rstrip("/") + "/skill/version"
    try:
        resp = urllib.request.urlopen(url, timeout=timeout)
    except urllib.error.HTTPError as e:
        if e.code == 404:
            die(f"Gateway {gateway_url} 没有携带 skill tarball (404)。"
                f"可能是旧版 Gateway,请联系发起人升级 Gateway。")
        die(f"Gateway 返回 HTTP {e.code}: {e.read().decode('utf-8', 'replace')[:200]}")
    except urllib.error.URLError as e:
        die(f"无法连接 Gateway {url}: {e}")
    raw = resp.read().decode("utf-8")
    data = json.loads(raw).get("data", {})
    if not data.get("version") or not data.get("sha256"):
        die(f"Gateway 响应异常:{raw[:200]}")
    return data


def _download(gateway_url: str, dest: pathlib.Path, expected_sha: str,
              timeout: float = 30.0) -> None:
    url = gateway_url.rstrip("/") + "/skill/download"
    info(f"下载 {url} -> {dest}")
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            with dest.open("wb") as f:
                shutil.copyfileobj(resp, f)
    except Exception as e:
        die(f"下载失败: {e}")

    # 校验 sha256
    h = hashlib.sha256()
    with dest.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    got = h.hexdigest()
    if got != expected_sha:
        dest.unlink(missing_ok=True)
        die(f"sha256 校验失败!expected={expected_sha} got={got}。"
            f"可能是下载中断或 Gateway 被篡改,已放弃升级。")
    ok(f"sha256 校验通过: {got[:16]}...")


def _stop_daemon() -> None:
    """调 daemon_stop 同款逻辑,确保 daemon 不占文件"""
    import _common  # 复用路径
    pid_file = _common.PID_FILE
    if not pid_file.exists():
        info("daemon 未运行,跳过停止")
        return
    try:
        pid = int(pid_file.read_text().strip())
    except Exception:
        info("PID 文件损坏,跳过停止")
        return
    try:
        os.kill(pid, 15)  # SIGTERM
    except ProcessLookupError:
        info(f"daemon pid {pid} 已不存在")
        pid_file.unlink(missing_ok=True)
        return
    # 等退
    for _ in range(30):  # 最多 3s
        try:
            os.kill(pid, 0)
            time.sleep(0.1)
        except ProcessLookupError:
            pid_file.unlink(missing_ok=True)
            ok(f"daemon (pid {pid}) 已停止")
            return
    # 硬杀
    try:
        os.kill(pid, 9)
    except Exception:
        pass
    pid_file.unlink(missing_ok=True)
    info(f"daemon (pid {pid}) 强制终止")


def _start_daemon_and_check(timeout: float = 15.0) -> bool:
    """拉起 daemon 并等健康检查通过,超时返回 False"""
    script = SKILL_ROOT / "scripts" / "ensure_daemon.py"
    py = sys.executable
    info("启动 daemon...")
    try:
        subprocess.run([py, str(script)], timeout=timeout, check=True,
                       capture_output=True, text=True)
        return True
    except subprocess.TimeoutExpired:
        return False
    except subprocess.CalledProcessError as e:
        print(f"daemon 启动失败: {e.stderr[:500]}", file=sys.stderr)
        return False


def _atomic_replace(new_tarball: pathlib.Path, skill_dir: pathlib.Path) -> pathlib.Path:
    """解压到 skill_dir.new, 然后 skill_dir -> skill_dir.old, skill_dir.new -> skill_dir
    返回备份目录路径,供失败回滚用"""
    new_dir = skill_dir.with_suffix(".new")
    old_dir = skill_dir.with_suffix(".old")

    # 清理残留
    if new_dir.exists():
        shutil.rmtree(new_dir)
    if old_dir.exists():
        shutil.rmtree(old_dir)

    # 解压
    new_dir.mkdir(parents=True)
    with tarfile.open(new_tarball, "r:gz") as tf:
        tf.extractall(new_dir)  # nosec: 本脚本只从受信 Gateway 下载 + sha256 校验过

    # 保留当前 venv(新包里不带)
    cur_venv = skill_dir / ".venv"
    if cur_venv.exists():
        shutil.move(str(cur_venv), str(new_dir / ".venv"))

    # 原子替换(mv 目录是原子的)
    shutil.move(str(skill_dir), str(old_dir))
    shutil.move(str(new_dir), str(skill_dir))
    return old_dir


def _rollback(skill_dir: pathlib.Path, backup_dir: pathlib.Path) -> None:
    info(f"回滚:{backup_dir} -> {skill_dir}")
    # 保住 .venv
    cur_venv = skill_dir / ".venv"
    if cur_venv.exists() and not (backup_dir / ".venv").exists():
        shutil.move(str(cur_venv), str(backup_dir / ".venv"))
    shutil.rmtree(skill_dir)
    shutil.move(str(backup_dir), str(skill_dir))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="只检查,不升级")
    ap.add_argument("--force", action="store_true", help="本地/远端版本相同也强制升级")
    ap.add_argument("--yes", action="store_true", help="非交互,默认同意")
    args = ap.parse_args()

    cfg = load_skill_config()
    gateway_url = cfg.get("gateway_url")
    if not gateway_url:
        die("skill 未初始化,请先 '初始化 Agent Gateway,地址 <url>'")

    local = _local_version()
    remote_info = _remote_version(gateway_url)
    remote = remote_info["version"]
    sha256 = remote_info["sha256"]

    print(f"本地版本: {local}")
    print(f"Gateway 版本: {remote}")

    if args.check:
        if local == remote:
            ok("已是最新")
            sys.exit(0)
        else:
            print("🔔 有新版可升级。执行 'python3 self_update.py' 或让 Claude 执行'升级 agent-gateway'")
            sys.exit(0)

    if local == remote and not args.force:
        ok("已是最新,无需升级")
        sys.exit(0)

    # 确认
    if not args.yes:
        reply = input(f"\n升级 {local} -> {remote}? [y/N] ").strip().lower()
        if reply != "y":
            print("取消。")
            sys.exit(0)

    # 下载到临时文件
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="skill-update-"))
    tarball = tmp / "skill.tar.gz"
    try:
        _download(gateway_url, tarball, sha256)

        # 停 daemon
        _stop_daemon()

        # 原子替换
        info("解压 + 原子替换...")
        backup = _atomic_replace(tarball, SKILL_ROOT)

        # 写版本号(以防 tarball 里忘了带)
        (SKILL_ROOT / "VERSION").write_text(remote + "\n")

        # 重启 daemon + 健康检查
        if not _start_daemon_and_check():
            print("❌ 新版 daemon 启动失败,回滚...", file=sys.stderr)
            _rollback(SKILL_ROOT, backup)
            _start_daemon_and_check()  # 尝试起回旧版
            die("已回滚到旧版,请联系发起人确认问题", code=2)

        # 成功后清理 .old
        if backup.exists():
            shutil.rmtree(backup)

        ok(f"升级完成: {local} -> {remote}")
        print("提示:如果有 agent 原本在线,已被升级中断,需要重新 '上线 xxx'")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    main()
