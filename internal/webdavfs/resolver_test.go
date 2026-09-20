package webdavfs

import (
	"context"
	"errors"
	"os"
	"testing"
)

// splitSegments：路径拆解与危险段拦截（纯逻辑，无 DB）。
func TestSplitSegments(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr error
	}{
		{"根路径", "/", nil, nil},
		{"空路径", "", nil, nil},
		{"单段", "/文档", []string{"文档"}, nil},
		{"多段", "/文档/项目/计划.docx", []string{"文档", "项目", "计划.docx"}, nil},
		{"URL 编码解码", "/my%20docs", []string{"my docs"}, nil},
		{"多余斜杠", "//a//b/", []string{"a", "b"}, nil},
		{"父目录穿越", "/a/../secret", nil, os.ErrNotExist},
		{"当前目录段", "/a/./b", nil, os.ErrNotExist},
		{"解码后含斜杠", "/a%2Fb", nil, os.ErrNotExist},
		{"非法编码", "/%", nil, os.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitSegments(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("splitSegments(%q) err = %v, want %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("splitSegments(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitSegments(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

// SegmentsToPath：href 回写编码（空格→%20 等），与 splitSegments 互逆。
func TestSegmentsToPath(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "/"},
		{[]string{"a"}, "/a"},
		{[]string{"my docs", "计划.docx"}, "/my%20docs/%E8%AE%A1%E5%88%92.docx"},
	}
	for _, tc := range cases {
		if got := SegmentsToPath(tc.in); got != tc.want {
			t.Errorf("SegmentsToPath(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveParent：父目录与末端名拆解（纯逻辑分支，FileRepo 为 nil——
// 触库的多段用例在 integration_test.go 里覆盖）。
func TestResolveParent(t *testing.T) {
	p := &PathResolver{}
	cases := []struct {
		name     string
		in       string
		wantLeaf string
		wantErr  error
	}{
		{"根不可写", "/", "", os.ErrInvalid},
		{"单段挂用户根", "/文档", "文档", nil},
		{"URL 编码单段", "/my%20docs", "my docs", nil},
		{"父目录穿越", "/a/../secret", "", os.ErrNotExist},
		{"当前目录段", "/a/./b", "", os.ErrNotExist},
		{"非法编码", "/%", "", os.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent, leaf, err := p.ResolveParent(context.Background(), 1, tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveParent(%q) err = %v, want %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if leaf != tc.wantLeaf {
				t.Fatalf("ResolveParent(%q) leaf = %q, want %q", tc.in, leaf, tc.wantLeaf)
			}
			if parent != nil {
				t.Fatalf("单段父节点应为 nil（用户根），got %v", parent)
			}
		})
	}
}

// roundtrip：编码回写的路径再次拆解应还原为原段。
func TestPathRoundtrip(t *testing.T) {
	segs := []string{"my docs", "计划+文档 v2.docx"}
	got, err := splitSegments(SegmentsToPath(segs))
	if err != nil {
		t.Fatalf("roundtrip split err: %v", err)
	}
	if len(got) != len(segs) {
		t.Fatalf("roundtrip len = %d, want %d", len(got), len(segs))
	}
	for i := range segs {
		if got[i] != segs[i] {
			t.Errorf("roundtrip[%d] = %q, want %q", i, got[i], segs[i])
		}
	}
}
