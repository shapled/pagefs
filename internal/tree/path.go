package tree

import (
	"fmt"
	"strings"

	"github.com/shapled/pagefs/internal/helper"
)

// Path 是树内的绝对路径表示。
type Path struct {
	Parts        []string
	EndWithSlash bool
}

// NewPath 解析以 / 开头的绝对路径；根路径 "/" 合法，中间出现空段（连续斜杠）视为非法。
func NewPath(name string) (*Path, error) {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("%s is not a valid path", name)
	}
	name = strings.TrimLeft(name, "/")
	if name == "" {
		return &Path{Parts: nil, EndWithSlash: true}, nil
	}
	endWithSlash := strings.HasSuffix(name, "/")
	if endWithSlash {
		name = strings.TrimRight(name, "/")
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" {
			return nil, fmt.Errorf("%s is not a valid path", name)
		}
	}
	return &Path{Parts: strings.Split(name, "/"), EndWithSlash: endWithSlash}, nil
}

func (p *Path) String() string {
	return "/" + strings.Join(p.Parts, "/") + helper.IfElse(p.EndWithSlash, "/", "")
}
