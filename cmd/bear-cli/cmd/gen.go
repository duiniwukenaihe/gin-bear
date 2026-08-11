package cmd

var genCmd = legacyCommand("gen")

// TemplateData is retained for v0.9.1 source compatibility.
// Deprecated: generation is implemented by internal/cli.
type TemplateData struct {
	PackageName string
	Title       string
	Name        string
	ModuleName  string
	Fields      []Field
	FieldsStr   string
	NeedsTime   bool
}

// Field is retained for v0.9.1 source compatibility.
// Deprecated: generation is implemented by internal/cli.
type Field struct {
	Name     string
	Type     string
	GoType   string
	JsonTag  string
	Validate string
	GormTag  string
}

// GetDefaultValue is retained for v0.9.1 source compatibility.
// Deprecated: generated resources no longer use template default literals.
func (f Field) GetDefaultValue() string {
	switch f.GoType {
	case "string":
		return `""`
	case "int64", "int", "int32", "int16", "int8":
		return "0"
	case "float64", "float32":
		return "0.0"
	case "bool":
		return "false"
	case "time.Time":
		return "time.Now()"
	default:
		return "nil"
	}
}
