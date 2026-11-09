"""Sreyun Agent 动作插件 —— 自定义运维动作库。

每个函数 = 一个可被 Sreyun 调用的动作。
从命令行调用：python sreyun_actions.py <action_name> [host_id] [args_json]

约定：
- 动作函数接受 (host_id: str, args: dict) 参数
- 返回字符串结果（打印到 stdout，供 Sreyun 引擎读取）
- 动作应快速返回，不要长期阻塞
- 异常会被引擎捕获并返回给 LLM

用法示例：
  python sreyun_actions.py restart_service worker-01 '{"service":"nginx"}'
  python sreyun_actions.py clear_cache worker-01 '{"path":"/tmp/cache"}'
  python sreyun_actions.py scale_pods worker-01 '{"replicas":3,"deployment":"api-gateway"}'
"""

import json
import sys
import os
import re
import subprocess
import urllib.parse
import urllib.request

# systemd 单元名：字母数字与 -@._: ，以及 systemd 转义用的反斜杠（如 dev-disk-by\x2duuid），
# 且不能以 '-' 开头（否则会被 systemctl 当成选项解析，例如 "--host=user@other" / "--all"）。
_UNIT_RE = re.compile(r"^[-A-Za-z0-9@._:\\]{1,128}$")

# clear_cache 允许清理的根目录（按真实路径判定）。
_CACHE_ROOTS = ("/tmp", "/var/cache")


def _valid_unit(name: str) -> bool:
    return bool(name) and not name.startswith("-") and bool(_UNIT_RE.match(name))


def _under_cache_root(real_path: str) -> bool:
    """real_path 必须是某个允许根目录的【子路径】（不含根目录本身）。

    旧实现用 abspath(...).startswith("/tmp")：既放过 /tmpX、/var/cache_evil 这类同前缀
    兄弟目录，又不解析符号链接——/tmp 人人可写，建一个 /tmp/x -> / 的链接，os.walk
    会跟进顶层链接，于是"清缓存"变成以服务进程权限递归删除整个文件系统。
    """
    for root in _CACHE_ROOTS:
        root_real = os.path.realpath(root)
        try:
            if os.path.commonpath([real_path, root_real]) == root_real and real_path != root_real:
                return True
        except ValueError:
            continue
    return False


def restart_service(host_id: str, args: dict) -> str:
    """重启指定服务"""
    service = args.get("service", "")
    if not service:
        return "错误：请指定服务名称 (service)"
    if not _valid_unit(service):
        return f"错误：非法的服务名称 {service!r}"
    # 通过 systemctl 重启（需要在 Agent 主机上执行）
    try:
        result = subprocess.run(
            ["systemctl", "restart", service],
            capture_output=True, text=True, timeout=30
        )
        if result.returncode == 0:
            return f"服务 {service} 重启成功"
        return f"服务 {service} 重启失败：{result.stderr.strip()}"
    except subprocess.TimeoutExpired:
        return f"服务 {service} 重启超时"
    except FileNotFoundError:
        return "systemctl 不可用，请确认在 Linux 主机上执行"


def clear_cache(host_id: str, args: dict) -> str:
    """清理指定路径的缓存"""
    path = args.get("path", "/tmp/cache")
    try:
        if not isinstance(path, str) or not path:
            return "错误：请指定要清理的路径 (path)"
        if not os.path.exists(path):
            return f"路径 {path} 不存在，无需清理"
        # 安全检查：只允许清理 /tmp 和 /var/cache 下的路径——按 realpath（解析全部符号链接）
        # 判定，且拒绝路径本身是符号链接；os.walk 默认不跟随子目录里的链接。
        if os.path.islink(path):
            return f"安全限制：{path} 是符号链接，拒绝清理"
        abs_path = os.path.realpath(path)
        if not _under_cache_root(abs_path):
            return f"安全限制：不允许清理 {path}，仅允许 /tmp 和 /var/cache 下的子目录"
        # 统计清理前的大小
        total_size = 0
        for root, dirs, files in os.walk(abs_path):
            for f in files:
                fp = os.path.join(root, f)
                try:
                    total_size += os.path.getsize(fp)
                except OSError:
                    pass
        # 清理
        for root, dirs, files in os.walk(abs_path, topdown=False):
            for f in files:
                try:
                    os.remove(os.path.join(root, f))
                except OSError:
                    pass
            for d in dirs:
                try:
                    os.rmdir(os.path.join(root, d))
                except OSError:
                    pass
        size_mb = total_size / (1024 * 1024)
        return f"缓存清理完成：{path} 释放了 {size_mb:.1f} MB"
    except Exception as e:
        return f"清理缓存失败：{e}"


def scale_pods(host_id: str, args: dict) -> str:
    """扩缩容 Kubernetes Deployment（经服务端 API，不再调用本机 kubectl）。

    推荐参数：cluster_id, deployment|name, namespace, replicas。
    也可设环境变量 AIOPS_URL + AIOPS_TOKEN（或 Cookie 会话由外部注入）。
    优先请 Sreyun 直接调用内置工具 k8s_scale。
    """
    replicas = args.get("replicas", 1)
    deployment = args.get("deployment") or args.get("name") or ""
    namespace = args.get("namespace", "default")
    cluster_id = args.get("cluster_id") or args.get("cluster") or ""
    if not deployment:
        return "错误：请指定 deployment 名称"
    if not cluster_id:
        return ("错误：请指定 cluster_id（面板「资源→K8s→集群配置」中的集群 ID）。"
                "本动作已改为调用服务端 /api/v1/k8s/.../scale，不再使用本机 kubectl。"
                "也可让助手直接调用工具 k8s_scale。")
    base = (os.environ.get("AIOPS_URL") or os.environ.get("AIOPS_PUBLIC_URL") or "http://127.0.0.1:8529").rstrip("/")
    token = os.environ.get("AIOPS_TOKEN") or os.environ.get("AIOPS_API_TOKEN") or ""
    try:
        replicas = int(replicas)
    except (TypeError, ValueError):
        return f"错误：replicas 必须是整数，得到 {replicas!r}"
    if replicas < 0:
        return "错误：replicas 不能为负数"
    # 路径段逐个转义：否则 deployment="../../../admin/..." 会把带令牌的 POST 打到任意 API 路径上。
    seg = lambda v: urllib.parse.quote(str(v), safe="")
    url = (f"{base}/api/v1/k8s/clusters/{seg(cluster_id)}/deployments/"
           f"{seg(namespace)}/{seg(deployment)}/scale")
    try:
        # urllib.request 在模块顶部导入：函数内的 `import urllib.request` 会把 urllib 变成
        # 本函数的局部名，上面的 seg() 在它执行前引用 urllib 会抛 UnboundLocalError。
        req = urllib.request.Request(
            url, data=json.dumps({"replicas": replicas}).encode("utf-8"),
            headers={"Content-Type": "application/json", **({"Authorization": f"Bearer {token}"} if token else {})},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=30) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            return f"Deployment {namespace}/{deployment} Scale 已提交（cluster={cluster_id}）：{body}"
    except Exception as e:
        return f"扩容失败：{e}（也可改用 Sreyun 工具 k8s_scale）"


def check_service_status(host_id: str, args: dict) -> str:
    """检查服务状态（只读）"""
    service = args.get("service", "")
    if not service:
        return "错误：请指定服务名称"
    if not _valid_unit(service):
        return f"错误：非法的服务名称 {service!r}"
    try:
        result = subprocess.run(
            ["systemctl", "status", service, "--no-pager"],
            capture_output=True, text=True, timeout=10
        )
        # 提取关键行
        lines = result.stdout.strip().split("\n")
        key_lines = [l.strip() for l in lines if l.strip() and (
            "Active:" in l or "Loaded:" in l or "Main PID:" in l or "Memory:" in l or "CGroup:" in l
        )]
        if key_lines:
            return f"服务 {service} 状态：\n" + "\n".join(key_lines[:5])
        return f"服务 {service} 状态：\n" + "\n".join(lines[:5])
    except FileNotFoundError:
        return "systemctl 不可用"


def list_actions() -> str:
    """列出所有可用动作"""
    return json.dumps({
        "actions": [
            {"name": "restart_service", "description": "重启指定服务", "params": ["service"]},
            {"name": "clear_cache", "description": "清理指定路径缓存", "params": ["path"]},
            {"name": "scale_pods", "description": "Kubernetes Pod 扩缩容", "params": ["replicas", "deployment", "namespace"]},
            {"name": "check_service_status", "description": "检查服务状态", "params": ["service"]},
        ]
    }, ensure_ascii=False)


# --- 注册表 ---
ACTIONS = {
    "restart_service": restart_service,
    "clear_cache": clear_cache,
    "scale_pods": scale_pods,
    "check_service_status": check_service_status,
    "list_actions": lambda h, a: list_actions(),
}


def main():
    if len(sys.argv) < 2:
        print(list_actions())
        return

    action_name = sys.argv[1]
    host_id = sys.argv[2] if len(sys.argv) > 2 else ""
    args_str = sys.argv[3] if len(sys.argv) > 3 else "{}"

    try:
        args = json.loads(args_str)
    except json.JSONDecodeError:
        args = {}

    action = ACTIONS.get(action_name)
    if not action:
        print(f"未知动作：{action_name}。可用动作：{', '.join(ACTIONS.keys())}")
        return

    result = action(host_id, args)
    print(result)


if __name__ == "__main__":
    main()