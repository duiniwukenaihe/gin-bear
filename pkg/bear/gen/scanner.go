package gen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
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
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}

	var results []InjectInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(s.Dir, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
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

				// Match the tag key exactly, the way the runtime does in
				// pkg/bear/ioc.go, which decides with field.Tag.Lookup("inject").
				// A substring test also matches `json:"inject_total"` and
				// `gorm:"column:inject_id"`, and the generated injector then
				// overwrote those fields with a resolved bean the runtime never
				// asked for.
				if _, inject := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("inject"); inject {
					// ExprString renders any type expression as Go source, so the
					// generated injector can paste it into bear.Resolve[T]. Formatting
					// the ast.Expr with %s instead yields fmt's debug form for every
					// composite type: "*Service" became
					// "&{%!s(token.Pos=90) Service}".
					typeName := types.ExprString(field.Type)
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

	return results, nil
}
