#!/usr/bin/env bash
# 本组件的示例数据，两部分：
#
# ① 应收台账样例（只要本组件与 NATS 在跑）：以 erp-sales 事件的形状往 NATS 发 6 条
#    sales.order.created.v1（order_id 以 seed-fin-order- 开头），由本组件真实的消费者
#    生成应收凭证、应收台账与已用额度；然后把到期日回填到 0–120 天前，并给一行写一笔
#    部分核销，让 GET /ar-ledger 的未核销余额与 GET /ar-ledger/summary 的四个账龄桶都有
#    东西可看。客户优先用 mdm-customer 种子里的 seed-customer-1..3（演示库里有就取它的
#    id 与名字），没有就用本脚本自己的 seed-fin-cust-1..3；客户名写进本组件的客户摘要
#    副本时 version 记 0，之后真实的 mdm.customer.* 事件（version ≥ 1）会覆盖它。
#    核销没有接口（收款流程还不存在），那一笔直接 UPDATE。
#
# ② 人工凭证与会计期间（经 REST + 真实 JWT，要求 infra/iam-casdoor 与 infra/authz 都在
#    项目里、各自的种子已灌）：给 dev.superuser（以及有的话 dev.finance.viewer）授权
#    default 法人；5 张人工凭证（5 个科目、单行 / 四行、一张被红字冲销）；三个历史期间
#    演示三态（2026-04 CLOSED→LOCKED、2026-05 CLOSED→OPEN、2026-07 CLOSED）。两者不在
#    项目里时这一部分整段跳过并说明原因。
#
# 幂等：消息按 (subject, aggregate_id, version) 去重、源单唯一约束兜底；REST 调用的幂等键
# 固定；回填写的是相对"现在"的绝对值。只给本地开发 / 演示用。
#
# 没有 seed-clean：entry_no 序列与每个期间的 post_no 计数器只增不减，LOCKED 是终态，
# 逐行 DELETE 回不到干净状态，要清空只能 make db-reset。
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
source "$ROOT/infra/scripts/lib/seed-net.sh"

need python3; need docker

# 宿主机上的 NATS（make up 起的 be-nats）；本组件容器经 host.docker.internal 连的是同一个。
SEED_NATS="${SEED_NATS:-localhost:4222}"
LEGAL_ENTITY="default"

# nats_pub <subject> <aggregate_id> <version> <payload>：照 be-sdk-go 的信封写消息头。
# 用 NATS 的文本协议直接发（HPUB），不需要另装 nats 命令行。
nats_pub() {
  python3 - "$SEED_NATS" "$@" <<'PY'
import socket, sys
hostport, subject, agg, ver, payload = sys.argv[1:6]
host, port = hostport.rsplit(":", 1)
s = socket.create_connection((host, int(port)), timeout=5)
f = s.makefile("rb")
if not f.readline().startswith(b"INFO"):
    sys.exit("NATS 没有回 INFO")
hdr = f"NATS/1.0\r\nX-Aggregate-Id: {agg}\r\nX-Version: {ver}\r\nX-Hop-Count: 0\r\n\r\n".encode()
body = payload.encode()
s.sendall(b'CONNECT {"verbose":false,"pedantic":false,"headers":true}\r\n')
s.sendall(f"HPUB {subject} {len(hdr)} {len(hdr) + len(body)}\r\n".encode() + hdr + body + b"\r\nPING\r\n")
line = f.readline()
while line.startswith(b"INFO"):
    line = f.readline()
if not line.startswith(b"PONG"):
    sys.exit(f"NATS 没有确认：{line!r}")
PY
}

sql1() { psqlx -tA -q -c "$1"; }

echo "── ① 应收台账样例：经 NATS 发 6 条 sales.order.created.v1（$SEED_NATS）──"
CUST_IDS=(); CUST_NAMES=()
for i in 1 2 3; do
  id="$(idfor mdm_customer "seed-customer-$i" 2>/dev/null || true)"
  if [ -n "$id" ]; then
    name="$(sql1 "SELECT name FROM mdm_customer.customers WHERE id = '$id'" 2>/dev/null || true)"
  fi
  if [ -z "$id" ] || [ -z "${name:-}" ]; then
    id="seed-fin-cust-$i"; name="「本地测试」财务样例客户 $i"
  fi
  CUST_IDS+=("$id"); CUST_NAMES+=("$name")
  sql1 "INSERT INTO erp_finance.customer_credit_snapshots (customer_id, name, version)
        VALUES ('$id', \$n\$$name\$n\$, 0) ON CONFLICT (customer_id) DO NOTHING" >/dev/null
done
ok "客户：${CUST_IDS[*]}（名字已进客户摘要副本，真实事件来了会覆盖）"

# 订单号 → 客户下标、金额、到期日距今天数（落在四个账龄桶里，含第 31 天这条边界）
ORDERS=(
  "1 0 12800.00 0"
  "2 0 3650.50 20"
  "3 1 22000.00 31"
  "4 1 980.25 45"
  "5 2 15420.00 75"
  "6 2 7300.00 120"
)
for o in "${ORDERS[@]}"; do
  read -r n c amount _days <<<"$o"
  oid="seed-fin-order-$n"
  nats_pub sales.order.created.v1 "$oid" 1 \
    "{\"order_id\":\"$oid\",\"order_no\":\"SEED-FIN-$n\",\"customer_id\":\"${CUST_IDS[$c]}\",\"items\":[],\"total_amount\":\"$amount\",\"version\":1}"
done

want=${#ORDERS[@]}
for _ in $(seq 1 30); do
  got="$(sql1 "SELECT count(*) FROM erp_finance.ar_ledger a JOIN erp_finance.finance_journal_entries e ON e.id = a.entry_id
              WHERE e.source_component = 'erp-sales' AND e.source_doc_id LIKE 'seed-fin-order-%'")"
  [ "$got" -ge "$want" ] && break
  sleep 1
done
[ "$got" -ge "$want" ] || die "等了 30 秒只看到 $got/$want 行应收：本组件的消费者没收到消息？看容器日志"
ok "应收：$got 行（每张订单一张应收凭证 借 1122 / 贷 6001）"

# 到期日与创建时间回填到 N 天前（created_at 一起挪，列表的默认 90 天窗口里只看得到较新的
# 几行，统计里全部都在）；订单 4 核销 300.00，未核销余额 680.25。
for o in "${ORDERS[@]}"; do
  read -r n _c _amount days <<<"$o"
  sql1 "UPDATE erp_finance.ar_ledger a SET due_date = current_date - $days, created_at = now() - interval '$days days'
        FROM erp_finance.finance_journal_entries e
        WHERE e.id = a.entry_id AND e.source_component = 'erp-sales' AND e.source_doc_id = 'seed-fin-order-$n'" >/dev/null
done
sql1 "UPDATE erp_finance.ar_ledger a SET reconciled_amount = 300.00
      FROM erp_finance.finance_journal_entries e
      WHERE e.id = a.entry_id AND e.source_component = 'erp-sales' AND e.source_doc_id = 'seed-fin-order-4'" >/dev/null
ok "到期日回填到 0 / 20 / 31 / 45 / 75 / 120 天前；订单 4 部分核销 300.00"
echo "   账龄（default 法人，按未核销余额）："
psqlx -q <<'SQL'
SELECT round(sum(amount - reconciled_amount) FILTER (WHERE current_date - due_date <= 30), 2)             AS d0_30,
       round(sum(amount - reconciled_amount) FILTER (WHERE current_date - due_date BETWEEN 31 AND 60), 2) AS d31_60,
       round(sum(amount - reconciled_amount) FILTER (WHERE current_date - due_date BETWEEN 61 AND 90), 2) AS d61_90,
       round(sum(amount - reconciled_amount) FILTER (WHERE current_date - due_date > 90), 2)              AS d90_plus
FROM erp_finance.ar_ledger WHERE legal_entity_id = 'default';
SQL

echo "── ② 人工凭证与会计期间（REST + 真实 JWT）──"
if [ -z "$(component_version infra/iam-casdoor)" ] || [ -z "$(component_version infra/authz)" ]; then
  echo "  跳过：infra/iam-casdoor 与 infra/authz 没有都加入项目，换不到 JWT（加入并灌好它们的种子后重跑本脚本）"
  exit 0
fi

seed_net_check
with_toolbox

FIN_REST="${FIN_REST:-http://$(service_name erp/finance):8087}"
SEED_USER="dev.superuser"
check_healthz "$FIN_REST/healthz" "erp-finance"

wait_bundle_refresh

ACCESS_TOKEN="$(get_app_jwt "$SEED_USER")"
ok "已换到 $SEED_USER 的应用 JWT"

authed() { curl -s -H "Authorization: Bearer $ACCESS_TOKEN" -H "Content-Type: application/json" "$@"; }

SEED_SUB="$(sub_of "$SEED_USER")"
[ -n "$SEED_SUB" ] || die "Casdoor 里找不到 $SEED_USER"

# 写路径（过账、关账）与读路径都按 legal_entity_access 过滤：不授权，superuser 也什么都
# 看不到、什么都写不进。
authed -X POST "$FIN_REST/erp/finance/legal-entity-access/$SEED_SUB" -d "{\"legal_entity_id\":\"$LEGAL_ENTITY\"}" >/dev/null
ok "legal_entity_access：$SEED_USER → $LEGAL_ENTITY"

# dev.finance.viewer 是 infra/authz 种的财务只读身份（只有 erp.finance.view）。不给它法人
# 授权，它登录进来看到的是空列表，与"数据范围没生效"分不清。没有这个用户就跳过。
FINANCE_VIEWER_SUB="$(sub_of dev.finance.viewer)"
if [ -n "$FINANCE_VIEWER_SUB" ]; then
  authed -X POST "$FIN_REST/erp/finance/legal-entity-access/$FINANCE_VIEWER_SUB" -d "{\"legal_entity_id\":\"$LEGAL_ENTITY\"}" >/dev/null
  ok "legal_entity_access：dev.finance.viewer → $LEGAL_ENTITY"
else
  echo "  （没找到 dev.finance.viewer：infra/authz 的种子身份还没建，跳过）"
fi

postentry() { # key lines_json memo -> entry id
  authed -X POST "$FIN_REST/erp/finance/entries" \
    -d "{\"idempotency_key\":\"$1\",\"legal_entity_id\":\"$LEGAL_ENTITY\",\"lines\":$2,\"memo\":\"$3\"}" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'
}
reverseentry() { # key entry_id reason -> reversal entry id
  authed -X POST "$FIN_REST/erp/finance/entries/$2/reverse" \
    -d "{\"idempotency_key\":\"$1\",\"reason\":\"$3\"}" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'
}
periodop() { # key period close|reopen|lock -> status
  authed -X POST "$FIN_REST/erp/finance/periods/$2/$3" \
    -d "{\"idempotency_key\":\"$1\",\"legal_entity_id\":\"$LEGAL_ENTITY\"}" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])'
}

# 人工凭证不接受调用方指定业务日期：全部落在今天所在的会计期间。
E1="$(postentry seed-fin-entry-1 \
  '[{"account_id":"1405","debit":"50000.00"},{"account_id":"2202","credit":"50000.00"}]' \
  "「本地测试」期初库存调整入账")"
E2="$(postentry seed-fin-entry-2 \
  '[{"account_id":"6401","debit":"8000.00"},{"account_id":"2202","credit":"8000.00"}]' \
  "「本地测试」预提运输费用")"
E3="$(postentry seed-fin-entry-3 \
  '[{"account_id":"1122","debit":"20000.00"},{"account_id":"6001","credit":"20000.00"}]' \
  "「本地测试」手工补录零售收入")"
# 四行：收入确认 + 结转成本合并成一张凭证，借贷按合计平衡（2 借 2 贷，各 32000）。
E4="$(postentry seed-fin-entry-4 \
  '[{"account_id":"1122","debit":"20000.00"},{"account_id":"6401","debit":"12000.00"},{"account_id":"6001","credit":"20000.00"},{"account_id":"1405","credit":"12000.00"}]' \
  "「本地测试」销售确认凭证（收入+结转成本合并示例，四行）")"
# 先过一笔"错"的，再红字冲销：原凭证一个字不动，产生一张借贷互换的新凭证。
E5="$(postentry seed-fin-entry-5 \
  '[{"account_id":"1405","debit":"3000.00"},{"account_id":"2202","credit":"3000.00"}]' \
  "「本地测试」误录库存调整（演示冲销）")"
E5_REVERSAL="$(reverseentry seed-fin-reverse-5 "$E5" "「本地测试」冲销：原凭证记错了金额")"
ok "凭证：E1=$E1 E2=$E2 E3=$E3 E4=$E4（四行） E5=$E5（已冲销 → $E5_REVERSAL）"

# 期间三态：用三个与"今天"无关的历史月份，起始状态都是 OPEN（别的月份不碰）。
# 2026-04 关账再锁定（LOCKED 是终态，必须先 CLOSED）；2026-05 关账再反关账（关账可逆）；
# 2026-07 只关账（已关账、还能反关账的中间态）。
periodop seed-fin-close-2026-04  2026-04 close  >/dev/null
periodop seed-fin-lock-2026-04   2026-04 lock   >/dev/null
periodop seed-fin-close-2026-05  2026-05 close  >/dev/null
periodop seed-fin-reopen-2026-05 2026-05 reopen >/dev/null
periodop seed-fin-close-2026-07  2026-07 close  >/dev/null
ok "期间：2026-04=LOCKED 2026-05=OPEN（走过一次 close→reopen） 2026-07=CLOSED"

# 给前 3 张凭证的 created_at / posted_at 回填到几天前，列表不全挤在同一秒。凭证头不分区，
# 改它安全；不碰 period，分录明细的分区归属不变。用 now() - interval 写绝对值，重跑不会
# 越跑越早。
psqlx -q <<SQL
SET search_path TO erp_finance;
UPDATE finance_journal_entries SET created_at = now() - interval '8 days', posted_at = now() - interval '8 days' WHERE id = $E1;
UPDATE finance_journal_entries SET created_at = now() - interval '5 days', posted_at = now() - interval '5 days' WHERE id = $E2;
UPDATE finance_journal_entries SET created_at = now() - interval '2 days', posted_at = now() - interval '2 days' WHERE id = $E3;
SQL
ok "已给 3 张凭证回填 created_at / posted_at（2–8 天前）"
