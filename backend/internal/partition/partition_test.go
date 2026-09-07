package partition

import (
	"testing"
	"time"
)

func TestMondayOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-09-06", "2026-08-31"}, // Sunday -> 上一周一
		{"2026-08-31", "2026-08-31"}, // Monday -> 自己
		{"2026-09-02", "2026-08-31"}, // Wednesday -> 本周一
	}
	for _, c := range cases {
		in, err := time.Parse("2006-01-02", c.in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := time.Parse("2006-01-02", c.want)
		if err != nil {
			t.Fatal(err)
		}
		got := mondayOf(in)
		if !got.Equal(want) {
			t.Errorf("mondayOf(%s) = %s，期望 %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
}
