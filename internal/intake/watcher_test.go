package intake

import "testing"

func TestClassifyCodec(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"hevc", "hevc"},
		{"HEVC", "hevc"},
		{"h265", "hevc"},
		{"x265", "hevc"},
		{"h264", "h264"},
		{"H264", "h264"},
		{"avc", "h264"},
		{"x264", "h264"},
		{"av1", "unknown"},
		{"vp9", "unknown"},
		{"mpeg4", "unknown"},
		{"", "unknown"},
	}

	for _, c := range cases {
		got := classifyCodec(c.raw)
		if got != c.want {
			t.Errorf("classifyCodec(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
