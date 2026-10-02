package repo

import (
	"fmt"
	"math/big"
	"regexp"
)

// 金额在本组件里一律是十进制字符串，列类型 NUMERIC(18,2)：整数部分最多 16 位、
// 小数最多 2 位。校验与运算全部换算成"分"的整数做——经过 float64 会在大额时
// 把一分钱的差抹平，"NaN"、"1e2" 之类的字面量也会混过校验。
var amountRe = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,2})?$`)

// signedDecimalRe 是数量（库存事件的 qty_delta）的格式：带符号、小数位数不限。
var signedDecimalRe = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

var hundred = big.NewInt(100)

// parseCents 把金额字符串换算成分；空串视为 0。格式不对返回 ErrInvalidArgument。
func parseCents(field, s string) (*big.Int, error) {
	if s == "" {
		return new(big.Int), nil
	}
	if !amountRe.MatchString(s) {
		return nil, fmt.Errorf("%w: %s 必须是非负、最多两位小数的十进制数：%q", ErrInvalidArgument, field, s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%w: %s 不是合法金额：%q", ErrInvalidArgument, field, s)
	}
	r.Mul(r, new(big.Rat).SetInt(hundred))
	return new(big.Int).Set(r.Num()), nil // 最多两位小数，乘 100 之后分母一定是 1
}

// formatCents 把分格式化成恰好两位小数的十进制字符串（与 NUMERIC(18,2) 的文本形式一致）。
func formatCents(c *big.Int) string {
	q, m := new(big.Int).QuoRem(new(big.Int).Abs(c), hundred, new(big.Int))
	sign := ""
	if c.Sign() < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%s.%02d", sign, q.String(), m.Int64())
}

// absRoundCents 取带符号数量的绝对值，四舍五入到分。
func absRoundCents(field, s string) (*big.Int, error) {
	if !signedDecimalRe.MatchString(s) {
		return nil, fmt.Errorf("%w: %s 不是合法数字：%q", ErrInvalidArgument, field, s)
	}
	r, _ := new(big.Rat).SetString(s)
	r.Abs(r).Mul(r, new(big.Rat).SetInt(hundred))
	// 四舍五入：floor(x + 1/2)
	r.Add(r, big.NewRat(1, 2))
	return new(big.Int).Quo(r.Num(), r.Denom()), nil
}
