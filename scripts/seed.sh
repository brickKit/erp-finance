#!/usr/bin/env bash
# 本组件自己的种子数据（总纲 SOP-W-7）：从零设计（AGENTS.md 原文点名
# "几乎没有专属演示数据"），覆盖 PostManualEntry 各种场景 + 会计期间
# 关账/反关账/锁定期间三个操作 + legal_entity 维数据权限授权。
#
# ⚠️ 只做单一法人 'default'（迁移播种数据里唯一存在的法人）——C17 记录
# 了"两个法人同期间各自首次过账会撞同一个 post_no"这个真实但当前不可
# 触发的设计缺口（阶段二/三都没有任何路径能创建第二个法人）。种子数据
# 刻意不去人为制造第二个法人来"顺便复现"C17：那需要绕开真实业务流程
# 直接插库，产生的第二法人不是这个系统任何真实功能能创建出来的东西，
# 演示数据没有必要为了触发一个已知记录、当前不可达的缺口而这样做。
#
# ⚠️ 实测确认的关键约束：PostManualEntry/ReverseEntry 都不接受调用方
# 指定 business_date——过账事务内部用 time.Now() 找覆盖"今天"的期间
# （backend/internal/repo/entry.go），所以本脚本建的凭证全部落在运行
# 脚本当天所在的会计期间（不是可以自由选期间）。这不是本脚本的限制，
# 是这个 rpc 契约本身的设计（人工补录的业务日期就是补录的当天）。
#
# 因此"时间跨度"这条 SOP-W-7 判据在本组件只能体现在两处：① 凭证的
# created_at/posted_at 在建完之后小幅回填（同 mdm-customer 的既有判据，
# finance_journal_entries 头表不分区，直接 UPDATE 安全——分区的是明细
# 表 finance_journal_entry_lines，且分区键是 period 字符串不是时间戳，
# 不受影响）；② 会计期间本身横跨 FY2026 全年 12 个月，关/锁几个跟"今天"
# 无关的历史期间，让期间状态本身有真实的新旧之分（即使当前没有查期间
# 列表的 rpc，见下方注释）。
#
# ⚠️ legal_entity_access 授权模式同 erp-inventory 的 warehouse_access
# （阶段三 Task 6 定案：分配数据归数据的宿主组件自己维护）——
# dev.superuser 必须先被授予 'default' 法人访问权限，PostManualEntry/
# ClosePeriod 等写路径才会放行（allowedLegalEntityIDs 校验，service.go）。
# 额外把这份访问权限也授予 dev.finance.viewer（infra-authz 种的财务只读
# 测试身份，角色 dev_finance_viewer，只有 erp.finance.view 一个权限
# 键）——不然这个"数据权限维度要有真实存在感"的账号登录进来会看到空
# 列表，跟没授权时的样子分不清。
#
# 全程走真实 REST + JWT 调用，不是直接写库；claim-first 幂等（固定
# idempotency_key），重复跑不会重复过账/重复转换期间状态。
#
# ⚠️ 只给本地开发/演示用，不出现在任何部署/CI 流程里。
#
# ⚠️ 没有 seed-clean.sh：跟 erp-inventory 同样的理由，而且更彻底——
# entry_no_seq/post_no 是 BIGSERIAL/期间内计数器，DELETE 不会让它们
# 回退（总纲 SOP-W-7"delete 不是 reset"判据）；更关键的是 LockPeriod
# 是**终态**，整个 rpc 契约里没有任何操作能把 LOCKED 转回去——一旦这
# 个脚本锁过某个期间，"删几行数据"从物理上就不可能把状态复原。想清空
# 只能 `make db-reset`（migrate down 再 up）。
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

C_GRN=$'\033[32m'; C_RED=$'\033[31m'; C_OFF=$'\033[0m'
ok()  { echo "${C_GRN}✓${C_OFF} $*"; }
die() { echo "${C_RED}✗${C_OFF} $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1"; }
need curl; need python3; need docker

CASDOOR_URL="${CASDOOR_URL:-http://localhost:8000}"
IAM_URL="${IAM_URL:-http://localhost:8200}"
FIN_REST="${FIN_REST:-http://localhost:8087}"
SEED_USER="dev.superuser"
SEED_PASSWORD="DevSeed123!"
SEED_APP="local-dev-seed-app"
LEGAL_ENTITY="default"
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

curl -sf -o /dev/null "$FIN_REST/healthz" || die "erp-finance（$FIN_REST）连不上，先 brickkit up"

psqlx() { docker exec -i be-postgres psql -U postgres -d brickkit_db -v ON_ERROR_STOP=1 "$@"; }

# infra-authz 的 bundle 是各组件每 ~15s 轮询一次拉进内存的——Makefile
# 链式调用刚跑完 infra-authz 的 seed 时，立刻拿 JWT 调自己的 REST 接口
# 有真实的竞态窗口（同 erp-inventory/crm-opportunity 的既有判据）。
echo "   等 18 秒，让本组件的权限 bundle 轮询到最新授权……"
sleep 18

echo "── 换一个真实 JWT，供调自己的 REST 接口用 ──"
curl -c "$COOKIE_JAR" -s -o /dev/null -X POST "$CASDOOR_URL/api/login" \
  -H "Content-Type: application/json" \
  -d '{"application":"app-built-in","organization":"built-in","username":"admin","password":"123","autoSignin":true,"type":"login"}'
APP_JSON="$(curl -b "$COOKIE_JAR" -s "$CASDOOR_URL/api/get-application?id=admin/$SEED_APP")"
# strict=False：见踩坑记录 C19（真实登录过一次后 customCss 字段带字面换行符）。
CLIENT_ID="$(echo "$APP_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin, strict=False)["data"]["clientId"])')"
CLIENT_SECRET="$(echo "$APP_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin, strict=False)["data"]["clientSecret"])')"

ID_TOKEN="$(curl -s -X POST "$CASDOOR_URL/api/login/oauth/access_token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=password" \
  --data-urlencode "username=$SEED_USER" \
  --data-urlencode "password=$SEED_PASSWORD" \
  --data-urlencode "client_id=$CLIENT_ID" \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  --data-urlencode "scope=openid profile email" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id_token"])')"
[ -n "$ID_TOKEN" ] || die "拿不到 Casdoor id_token"

ACCESS_TOKEN="$(curl -s -X POST "$IAM_URL/api/iam/token" \
  -H "Content-Type: application/json" \
  -d "{\"casdoor_id_token\": \"$ID_TOKEN\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')"
[ -n "$ACCESS_TOKEN" ] || die "换应用 JWT 失败"
ok "已换到真实应用 JWT"

authed() { curl -s -H "Authorization: Bearer $ACCESS_TOKEN" -H "Content-Type: application/json" "$@"; }

USER_JSON="$(curl -b "$COOKIE_JAR" -s "$CASDOOR_URL/api/get-user?id=brickkit/$SEED_USER")"
SEED_SUB="$(echo "$USER_JSON" | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d["id"] if d else "")')"
[ -n "$SEED_SUB" ] || die "Casdoor 里找不到 $SEED_USER"

echo "── 给 dev.superuser 授权 $LEGAL_ENTITY 法人访问权限（幂等）──"
authed -X POST "$FIN_REST/erp/finance/legal-entity-access/$SEED_SUB" -d "{\"legal_entity_id\":\"$LEGAL_ENTITY\"}" >/dev/null
ok "legal_entity_access 已就绪（dev.superuser）"

# ⚠️ 数据权限维度要有真实存在感（总纲 SOP-W-7）：infra-authz 种的财务
# 只读测试用户（dev.finance.viewer，角色 dev_finance_viewer，只有
# erp.finance.view 一个权限键）如果没有任何 legal_entity_access，登录
# 进来会看到空列表，跟"这条数据权限维度根本没生效"分不清。同 erp-inventory
# 对 dev.warehouse.south 的既有判据：独立查这个用户名的 sub，查不到就
# 说明 infra-authz 的种子身份还没跑，优雅跳过不中断本脚本其余步骤。
FINANCE_VIEWER_SUB="$(curl -b "$COOKIE_JAR" -s "$CASDOOR_URL/api/get-user?id=brickkit/dev.finance.viewer" | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d["id"] if d else "")')"
if [ -n "$FINANCE_VIEWER_SUB" ]; then
  authed -X POST "$FIN_REST/erp/finance/legal-entity-access/$FINANCE_VIEWER_SUB" -d "{\"legal_entity_id\":\"$LEGAL_ENTITY\"}" >/dev/null
  ok "legal_entity_access 已就绪（dev.finance.viewer → $LEGAL_ENTITY，只读角色终于有真实数据可看）"
else
  echo "  （没探测到 dev.finance.viewer——不是依赖，只是 infra-authz 的种子身份还没建，跳过）"
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

echo "── PostManualEntry：5 张人工凭证，覆盖 5 个科目 + 单行/多行 + 待冲销样例 ──"
E1="$(postentry seed-fin-entry-1 \
  '[{"account_id":"1405","debit":"50000.00"},{"account_id":"2202","credit":"50000.00"}]' \
  "「本地测试」期初库存调整入账")"
E2="$(postentry seed-fin-entry-2 \
  '[{"account_id":"6401","debit":"8000.00"},{"account_id":"2202","credit":"8000.00"}]' \
  "「本地测试」预提运输费用")"
E3="$(postentry seed-fin-entry-3 \
  '[{"account_id":"1122","debit":"20000.00"},{"account_id":"6001","credit":"20000.00"}]' \
  "「本地测试」手工补录零售收入")"
# 四行示例：收入确认 + 结转成本合并做一张凭证——比两行的示例更贴近真实
# 业务场景，也验证了 validateBalanced 是按总借贷合计平衡，不是按行两两
# 配对（2 借 2 贷，合计各 32000）。
E4="$(postentry seed-fin-entry-4 \
  '[{"account_id":"1122","debit":"20000.00"},{"account_id":"6401","debit":"12000.00"},{"account_id":"6001","credit":"20000.00"},{"account_id":"1405","credit":"12000.00"}]' \
  "「本地测试」销售确认凭证（收入+结转成本合并示例，四行）")"
# 待冲销样例：先过一笔"错"的，再红字冲销掉，验证 ReverseEntry 全链路
# （原凭证一个字不动，产生一张新的反向凭证）。
E5="$(postentry seed-fin-entry-5 \
  '[{"account_id":"1405","debit":"3000.00"},{"account_id":"2202","credit":"3000.00"}]' \
  "「本地测试」误录库存调整（演示冲销）")"
E5_REVERSAL="$(reverseentry seed-fin-reverse-5 "$E5" "「本地测试」冲销：原凭证记错了金额")"

ok "凭证：E1=$E1 E2=$E2 E3=$E3 E4=$E4(四行) E5=$E5(已冲销→$E5_REVERSAL)"

echo "── 会计期间生命周期：挑几个跟"今天"无关的历史期间演示三态流转 ──"
# ⚠️ 实测踩坑：本来想用 2026-01/02/03 三个月演示，真机一跑发现当时
# 2026-01=LOCKED、2026-02=CLOSED、2026-06=CLOSED、2026-11=CLOSED——是
# Task 6 实现期间人工验证 ClosePeriod/LockPeriod 时真实留在 brickkit_db
# （演示库）里的历史状态（早于本脚本存在，也早于 E3 测试库/演示库分离
# 这条判据落地），不是本脚本造成的脏数据；`make db-reset` 之后这份历史
# 状态会被清空，那几个月会变回默认 OPEN。改用 2026-04/05/07 三个月，
# 不管数据库处在哪种历史状态下都不会跟别的期间操作打架——这是本脚本
# 自己稳定拥有、可预期起始状态是 OPEN 的三个月。
#
# 2026-04：CLOSE 再 LOCK——终态演示（LockPeriod 要求先经过 CLOSED，
# 不能从 OPEN 直接跳过去，period.go 的 transitionPeriod 状态机）。
periodop seed-fin-close-2026-04 2026-04 close >/dev/null
periodop seed-fin-lock-2026-04  2026-04 lock  >/dev/null
# 2026-05：CLOSE 再 REOPEN——演示"关账是可逆的日常操作"这条设计判据
# （最终状态又回到 OPEN，但真实走过了一次 close→reopen 的往返）。
periodop seed-fin-close-2026-05  2026-05 close  >/dev/null
periodop seed-fin-reopen-2026-05 2026-05 reopen >/dev/null
# 2026-07：只 CLOSE，不 LOCK——留一个"已关账但还能反关账"的中间态样例。
periodop seed-fin-close-2026-07 2026-07 close >/dev/null
ok "期间状态（本脚本操作过的月份）：2026-04=LOCKED 2026-05=OPEN（曾 close→reopen） 2026-07=CLOSED；其余月份维持当前实际状态（默认 OPEN，除非之前有别的操作动过）"

# ── 时间跨度回填：所有凭证都发生在"今天"所在的会计期间（PostManualEntry
# 不接受调用方指定 business_date，见脚本顶部注释），直接给前 3 张凭证的
# created_at/posted_at 回填到本月更早几天，列表默认按时间排序时不是
# 全部凭证都挤在同一秒。finance_journal_entries 头表不分区，安全；
# 不碰 period 字段本身，不影响 finance_journal_entry_lines 的分区归属。
#
# ⚠️ 用 `now() - interval` 写绝对值，不是 `created_at - interval`——
# 后者每重跑一次脚本就会在上一次已经回填过的值基础上再减一次，越跑
# 越早，不是幂等（同 mdm-customer/mdm-product 已验证过的既有判据：
# 目标值必须是相对"运行脚本这一刻"算出来的绝对值，不能相对列的当前值）。
psqlx -q <<SQL
SET search_path TO erp_finance;
UPDATE finance_journal_entries SET created_at = now() - interval '8 days', posted_at = now() - interval '8 days' WHERE id = $E1;
UPDATE finance_journal_entries SET created_at = now() - interval '5 days', posted_at = now() - interval '5 days' WHERE id = $E2;
UPDATE finance_journal_entries SET created_at = now() - interval '2 days', posted_at = now() - interval '2 days' WHERE id = $E3;
SQL
ok "已给 3 张凭证回填历史 created_at/posted_at（当月内提前 2-8 天）"
