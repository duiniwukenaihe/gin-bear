package gen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// InjectInfo 包含需要注入的信息
type InjectInfo struct {
	StructName string
	Fields     []FieldInfo
}

// FieldInfo 包含字段信息
type FieldInfo struct {
	FieldName string
	TypeName  string
}

// Scanner 扫描目录下的 Go 文件
type Scanner struct {
	Dir string
}

func NewScanner(dir string) *Scanner {
	return &Scanner{Dir: dir}
}

// Scan 扫描并识别带有 inject 标签的结构体
func (s *Scanner) Scan() ([]InjectInfo, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, s.Dir, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var results []InjectInfo

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}

				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}

				info := InjectInfo{
					StructName: ts.Name.Name,
				}

				for _, field := range st.Fields.List {
					if field.Tag == nil {
						continue
					}

					tag := strings.Trim(field.Tag.Value, "`")
					if strings.Contains(tag, "inject") {
						typeName := fmt.Sprintf("%s", field.Type)
						// 处理简单的类型名提取，如果是指针等复杂类型可能需要更复杂的逻辑
						// 这里先简化处理
						for _, name := range field.Names {
							info.Fields = append(info.Fields, FieldInfo{
								FieldName: name.Name,
								TypeName:  typeName,
							})
						}
					}
				}

				if len(info.Fields) > 0 {
					results = append(results, info)
				}

				return true
			})
		}
	}

	return results, nil
}
