package repo

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cursor 编码 (created_at, id)：keyset 分页，不用 offset（深分页慢、翻页时会漏行）。
type cursorKey struct {
	CreatedAt time.Time
	ID        int64
}

func encodeCursor(k cursorKey) string {
	raw := k.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(k.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursorKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorKey{}, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return cursorKey{}, fmt.Errorf("格式不对：%q", string(raw))
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return cursorKey{}, err
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return cursorKey{}, err
	}
	return cursorKey{CreatedAt: t, ID: id}, nil
}

// parseCursor 解析列表请求带来的游标；空串表示第一页（返回 nil）。解不开的
// 游标是调用方的参数错误（400），不是服务端故障。
func parseCursor(s string) (*cursorKey, error) {
	if s == "" {
		return nil, nil
	}
	k, err := decodeCursor(s)
	if err != nil {
		return nil, fmt.Errorf("%w: 非法 cursor：%v", ErrInvalidArgument, err)
	}
	return &k, nil
}
