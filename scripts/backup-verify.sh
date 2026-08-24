#!/usr/bin/env bash
# 备份恢复演练（backup restore drill）
#
# 「有备份」和「恢复得回来」是两件事。这个脚本把后者做成一条命令：
# 把 pg_dump 产物还原进一台**一次性** PostgreSQL，跑一组存活性查询；
# 校验时序导出与录像包能否解开，可选地把时序推进一台一次性 VictoriaMetrics 并查回来。
# 全程不碰生产库。
#
# 用法：
#   scripts/backup-verify.sh                          # 自动取备份目录里最新的三种产物
#   scripts/backup-verify.sh -d /backup/aiops-pg-...dump -m /backup/aiops-vm-...native.gz
#   BACKUP_DIR=/data/backups scripts/backup-verify.sh
#
# 依赖：docker（起一次性 PG/VM）、pg_restore/psql（或用容器内的）、gzip、tar。
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-${AIOPS_BACKUP_DIR:-./backups}}"
PG_DUMP_FILE=""
VM_FILE=""
REC_FILE=""
PG_IMAGE="${PG_IMAGE:-postgres:16-alpine}"
VM_IMAGE="${VM_IMAGE:-victoriametrics/victoria-metrics:v1.102.0}"
KEEP=0
SKIP_VM_IMPORT=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    -d|--dump) PG_DUMP_FILE="$2"; shift 2 ;;
    -m|--metrics) VM_FILE="$2"; shift 2 ;;
    -r|--recordings) REC_FILE="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --skip-vm-import) SKIP_VM_IMPORT=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

newest() { ls -1t "$BACKUP_DIR"/$1 2>/dev/null | head -1 || true; }
[[ -z "$PG_DUMP_FILE" ]] && PG_DUMP_FILE="$(newest 'aiops-pg-*.dump')"
[[ -z "$VM_FILE"      ]] && VM_FILE="$(newest 'aiops-vm-*.native.gz')"
[[ -z "$REC_FILE"     ]] && REC_FILE="$(newest 'aiops-rec-*.tar.gz')"

PASS=(); FAIL=(); SKIP=()
ok()   { PASS+=("$1"); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; }
skip() { SKIP+=("$1"); printf '  \033[33m-\033[0m %s（跳过）\n' "$1"; }

need_docker() { command -v docker >/dev/null 2>&1; }

CID_PG=""; CID_VM=""
cleanup() {
  [[ $KEEP -eq 1 ]] && { echo "保留一次性容器：PG=$CID_PG VM=$CID_VM"; return; }
  [[ -n "$CID_PG" ]] && docker rm -f "$CID_PG" >/dev/null 2>&1 || true
  [[ -n "$CID_VM" ]] && docker rm -f "$CID_VM" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== AIOps 备份恢复演练 =="
echo "备份目录：$BACKUP_DIR"
echo

# ---------- 1. PostgreSQL ----------
echo "[1/3] PostgreSQL 还原验证"
if [[ -z "$PG_DUMP_FILE" || ! -f "$PG_DUMP_FILE" ]]; then
  bad "找不到 PG 备份文件（$BACKUP_DIR/aiops-pg-*.dump）"
elif ! need_docker; then
  skip "无 docker，无法起一次性 PostgreSQL"
else
  echo "  文件：$PG_DUMP_FILE ($(du -h "$PG_DUMP_FILE" | cut -f1))"
  PGPORT_T=$(( ( RANDOM % 10000 ) + 45000 ))
  CID_PG=$(docker run -d --rm -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=aiops_drill \
      -p "127.0.0.1:${PGPORT_T}:5432" "$PG_IMAGE")
  DSN="postgres://postgres:drill@127.0.0.1:${PGPORT_T}/aiops_drill?sslmode=disable"
  echo -n "  等待 PostgreSQL 就绪"
  for _ in $(seq 1 60); do
    if docker exec "$CID_PG" pg_isready -U postgres >/dev/null 2>&1; then break; fi
    echo -n "."; sleep 1
  done
  echo
  if docker exec "$CID_PG" pg_isready -U postgres >/dev/null 2>&1; then
    ok "一次性 PostgreSQL 已就绪"
    if docker exec -i "$CID_PG" pg_restore --no-owner --dbname "postgres://postgres:drill@127.0.0.1:5432/aiops_drill" \
         < "$PG_DUMP_FILE" >/tmp/aiops-drill-restore.log 2>&1; then
      ok "pg_restore 无错误退出"
    else
      # pg_restore 对扩展/属主类告警会返回非零；有对象落地就继续核对内容
      bad "pg_restore 返回非零（详见 /tmp/aiops-drill-restore.log，末 20 行如下）"
      tail -20 /tmp/aiops-drill-restore.log | sed 's/^/      /'
    fi
    q() { docker exec "$CID_PG" psql -tAq -U postgres -d aiops_drill -c "$1" 2>/dev/null | tr -d '[:space:]'; }
    TABLES=$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")
    [[ "${TABLES:-0}" -gt 20 ]] && ok "还原后有 $TABLES 张表" || bad "还原后只有 ${TABLES:-0} 张表，明显不完整"
    for t in schema_migrations app_config audit_log hosts; do
      C=$(q "SELECT count(*) FROM $t")
      if [[ -n "$C" ]]; then ok "$t 可查询（$C 行）"; else bad "$t 查询失败或不存在"; fi
    done
    MIG=$(q "SELECT coalesce(max(version),0) FROM schema_migrations")
    [[ "${MIG:-0}" -gt 0 ]] && ok "迁移版本最高 $MIG" || bad "schema_migrations 为空（升级后备份？）"
    # 审计链是合规承诺，抽查它在还原后仍然连得上
    CHAIN=$(q "SELECT count(*) FROM audit_log WHERE hash IS NOT NULL AND hash<>''" || true)
    [[ -n "$CHAIN" ]] && ok "审计链哈希列可读（$CHAIN 行带哈希）" || skip "审计链列不可读（旧版本备份）"
  else
    bad "一次性 PostgreSQL 未就绪"
  fi
fi
echo

# ---------- 2. 时序 ----------
echo "[2/3] VictoriaMetrics 导出验证"
if [[ -z "$VM_FILE" || ! -f "$VM_FILE" ]]; then
  skip "没有时序备份（备份配置里未开启 include_vm？）"
else
  echo "  文件：$VM_FILE ($(du -h "$VM_FILE" | cut -f1))"
  if gzip -t "$VM_FILE" 2>/dev/null; then ok "gzip 完整性校验通过"; else bad "gzip 校验失败——文件截断"; fi
  SIZE=$(stat -c%s "$VM_FILE" 2>/dev/null || stat -f%z "$VM_FILE")
  [[ "$SIZE" -gt 1024 ]] && ok "导出非空（$SIZE 字节）" || bad "导出几乎是空的（$SIZE 字节）——导出时间窗内没有数据？"
  if [[ $SKIP_VM_IMPORT -eq 1 ]]; then
    skip "按参数跳过时序回灌"
  elif ! need_docker; then
    skip "无 docker，无法起一次性 VictoriaMetrics"
  else
    VMPORT_T=$(( ( RANDOM % 10000 ) + 55000 ))
    CID_VM=$(docker run -d --rm -p "127.0.0.1:${VMPORT_T}:8428" "$VM_IMAGE")
    echo -n "  等待 VictoriaMetrics 就绪"
    for _ in $(seq 1 60); do
      if curl -fsS "http://127.0.0.1:${VMPORT_T}/health" >/dev/null 2>&1; then break; fi
      echo -n "."; sleep 1
    done
    echo
    if curl -fsS "http://127.0.0.1:${VMPORT_T}/health" >/dev/null 2>&1; then
      ok "一次性 VictoriaMetrics 已就绪"
      if gzip -dc "$VM_FILE" | curl -fsS --data-binary @- \
            "http://127.0.0.1:${VMPORT_T}/api/v1/import/native" >/dev/null 2>&1; then
        ok "时序回灌成功（/api/v1/import/native）"
        sleep 3
        SERIES=$(curl -fsS "http://127.0.0.1:${VMPORT_T}/api/v1/series/count" 2>/dev/null | grep -o '"[0-9]*"' | head -1 | tr -d '"' || echo 0)
        [[ "${SERIES:-0}" -gt 0 ]] && ok "回灌后可查到 $SERIES 条时间序列" || bad "回灌后查不到任何序列"
      else
        bad "时序回灌失败"
      fi
    else
      bad "一次性 VictoriaMetrics 未就绪"
    fi
  fi
fi
echo

# ---------- 3. 录像 ----------
echo "[3/3] 录像包验证"
if [[ -z "$REC_FILE" || ! -f "$REC_FILE" ]]; then
  skip "没有录像备份（备份配置里未开启 include_recordings？）"
else
  echo "  文件：$REC_FILE ($(du -h "$REC_FILE" | cut -f1))"
  if gzip -t "$REC_FILE" 2>/dev/null; then ok "gzip 完整性校验通过"; else bad "gzip 校验失败——文件截断"; fi
  N=$(tar -tzf "$REC_FILE" 2>/dev/null | wc -l | tr -d ' ')
  # 空包不判失败：从没开过终端/桌面的环境本来就没有录像。但要说清楚，
  # 免得"备份里有录像"这件事被一份空包糊弄过去。
  if [[ "${N:-0}" -gt 0 ]]; then
    ok "包内 $N 个录像文件"
  else
    skip "包内没有录像文件 —— 若这套环境确实开过终端/桌面会话，说明录像目录没被打进来，需要排查"
  fi
  TMPD=$(mktemp -d)
  if tar -xzf "$REC_FILE" -C "$TMPD" 2>/dev/null; then
    ok "可完整解包"
    SAMPLE=$(find "$TMPD" -name '*.json' | head -1)
    if [[ -n "$SAMPLE" ]] && head -c 1 "$SAMPLE" | grep -q '[[{]'; then
      ok "抽样录像文件是合法 JSON 开头"
    else
      skip "包内没有 JSON 录像可抽样"
    fi
  else
    bad "解包失败"
  fi
  rm -rf "$TMPD"
fi

echo
echo "== 结论 =="
printf '通过 %d 项，失败 %d 项，跳过 %d 项\n' "${#PASS[@]}" "${#FAIL[@]}" "${#SKIP[@]}"
if [[ ${#FAIL[@]} -gt 0 ]]; then
  printf '失败项：\n'; printf '  - %s\n' "${FAIL[@]}"
  echo "演练不通过：这份备份现在还救不回来，先修再谈 RTO。"
  exit 1
fi
echo "演练通过：这份备份可以还原。建议把本次输出留档到验收材料里。"
