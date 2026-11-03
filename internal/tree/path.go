package tree

import (
	"fmt"
	"strings"

	"github.com/shapled/pagefs/internal/helper"
)

const (
	rootName     = "pagefs"
	tabsListName = "tabs.list"
	tabsName     = "tabs"
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

func (p *Path) IsRoot() bool {
	return len(p.Parts) == 0
}

func (p *Path) IsTabs() bool {
	return len(p.Parts) == 1 && p.Parts[0] == tabsName
}

func (p *Path) IsTabsList() bool {
	return len(p.Parts) == 1 && p.Parts[0] == tabsListName && !p.EndWithSlash
}

func (p *Path) IsSubOfTabs() bool {
	return len(p.Parts) >= 1 && p.Parts[0] == tabsName
}

// IsTabDir 精确匹配 /tabs/<id>（目录，允许尾斜杠）。
func (p *Path) IsTabDir() bool {
	return len(p.Parts) == 2 && p.Parts[0] == tabsName
}

// IsTabFile 精确匹配 /tabs/<id>/<file>（文件，不接受尾斜杠）。
func (p *Path) IsTabFile() bool {
	return len(p.Parts) == 3 && p.Parts[0] == tabsName && !p.EndWithSlash
}

// TabID 返回 /tabs/<id>[/file] 中的 id；不匹配时返回空串。
func (p *Path) TabID() string {
	if len(p.Parts) >= 2 && p.Parts[0] == tabsName {
		return p.Parts[1]
	}
	return ""
}

// TabFile 返回 /tabs/<id>/<file> 中的 file；不匹配时返回空串。
func (p *Path) TabFile() string {
	if len(p.Parts) == 3 && p.Parts[0] == tabsName {
		return p.Parts[2]
	}
	return ""
}
