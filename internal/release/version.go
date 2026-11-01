package release

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 版本决策错误。
var (
	// ErrInvalidVersion 表示版本号不是 x.y.z 形式。
	ErrInvalidVersion = errors.New("非法语义版本")
	// ErrSameVersion 表示目标版本与当前相同。
	ErrSameVersion = errors.New("目标版本与当前版本相同")
	// ErrDowngradeDenied 表示目标更旧且未显式允许降级。
	ErrDowngradeDenied = errors.New("拒绝降级：目标版本低于当前版本")
)

// Version 是简化语义版本（major.minor.patch）。
type Version struct {
	Major int
	Minor int
	Patch int
}

// Parse 解析 x.y.z，允许单个前缀 v/V；不接受预发布/构建元数据。
func Parse(text string) (Version, error) {
	v := strings.TrimSpace(text)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q 必须为 x.y.z", ErrInvalidVersion, text)
	}
	out := Version{}
	values := []*int{&out.Major, &out.Minor, &out.Patch}
	for i, p := range parts {
		if p == "" || strings.ContainsAny(p, "-+") {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, text)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%w: %q 段必须为非负整数", ErrInvalidVersion, text)
		}
		*values[i] = n
	}
	return out, nil
}

// Compare 比较两版本：-1 a<b，0 相等，1 a>b。
func Compare(a, b string) (int, error) {
	va, err := Parse(a)
	if err != nil {
		return 0, err
	}
	vb, err := Parse(b)
	if err != nil {
		return 0, err
	}
	switch {
	case va.Major != vb.Major:
		return boolToCmp(va.Major > vb.Major), nil
	case va.Minor != vb.Minor:
		return boolToCmp(va.Minor > vb.Minor), nil
	case va.Patch != vb.Patch:
		return boolToCmp(va.Patch > vb.Patch), nil
	default:
		return 0, nil
	}
}

func boolToCmp(greater bool) int {
	if greater {
		return 1
	}
	return -1
}

// DecideUpgrade 校验从 current 升级到 target 是否允许。
// 相同版本返回 ErrSameVersion；降级且未显式允许返回 ErrDowngradeDenied。
func DecideUpgrade(current, target string, allowDowngrade bool) error {
	cmp, err := Compare(current, target)
	if err != nil {
		return err
	}
	switch cmp {
	case 0:
		return ErrSameVersion
	case 1:
		if !allowDowngrade {
			return fmt.Errorf("%w: %s -> %s", ErrDowngradeDenied, current, target)
		}
	}
	return nil
}
