package repo

import (
	"context"
	"testing"
)

// 下一个会计年度必须在 1 月 1 日之前开好：业务日期落在 2027 年的凭证（包括每条
// 销售订单事件生成的应收凭证）要记进 2027 的期间，而不是因为找不到期间而失败——
// 事件消费是至多一次，失败的消息不会再来。
func TestPostEntryTx_2027年的业务日期记进FY2027的期间(t *testing.T) {
	db := testDB(t)
	entry, err := postEntryTx(context.Background(), mustBeginTx(t, db), postEntryTxInput{
		LegalEntityID: "default",
		BusinessDate:  mustParseDate(t, "2027-01-15"),
		Lines:         []Line{{AccountCode: "1122", Debit: "10"}, {AccountCode: "6001", Credit: "10"}},
	})
	if err != nil {
		t.Fatalf("2027-01-15 的凭证应该能过账，实际：%v", err)
	}
	if entry.Period != "2027-01" {
		t.Fatalf("期望记进 2027-01，实际 %q", entry.Period)
	}
}
