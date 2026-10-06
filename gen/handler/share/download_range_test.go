package share

import "testing"

func TestParseByteRange(t *testing.T) {
	const size = 1000
	cases := []struct {
		name       string
		header     string
		size       int64
		wantStart  int64
		wantLength int64
		wantResult rangeParseResult
	}{
		{"无 Range 头", "", size, 0, 0, rangeNone},
		{"非 bytes 单位", "items=0-5", size, 0, 0, rangeNone},
		{"标准区间", "bytes=0-4", size, 0, 5, rangeOK},
		{"中段区间", "bytes=10-19", size, 10, 10, rangeOK},
		{"末段区间", "bytes=995-999", size, 995, 5, rangeOK},
		{"end 越界截断", "bytes=10-99999", size, 10, 990, rangeOK},
		{"开区间到末尾", "bytes=500-", size, 500, 500, rangeOK},
		{"suffix 末 N 字节", "bytes=-100", size, 900, 100, rangeOK},
		{"suffix 大于文件", "bytes=-5000", size, 0, 1000, rangeOK},
		{"suffix 为零", "bytes=-0", size, 0, 0, rangeNotSatisfiable},
		{"start 越界", "bytes=1000-", size, 0, 0, rangeNotSatisfiable},
		{"start 超越 size", "bytes=2000-2999", size, 0, 0, rangeNotSatisfiable},
		{"空文件任何区间", "bytes=0-0", 0, 0, 0, rangeNotSatisfiable},
		{"多区间忽略", "bytes=0-4,10-19", size, 0, 0, rangeNone},
		{"start 大于 end", "bytes=5-3", size, 0, 0, rangeNone},
		{"非数字", "bytes=a-b", size, 0, 0, rangeNone},
		{"缺横杠", "bytes=5", size, 0, 0, rangeNone},
		{"负 start", "bytes=-1-5", size, 0, 0, rangeNone},
		{"带空格容忍", "bytes= 0-4 ", size, 0, 5, rangeOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, length, res := parseByteRange(tc.header, tc.size)
			if res != tc.wantResult || start != tc.wantStart || length != tc.wantLength {
				t.Fatalf("parseByteRange(%q, %d) = (%d, %d, %v), want (%d, %d, %v)",
					tc.header, tc.size, start, length, res, tc.wantStart, tc.wantLength, tc.wantResult)
			}
		})
	}
}
