package bear

import (
	"fmt"
	"strings"
)

// Value 用于从配置中读取值
// 使用方式:
//
//	type MyController struct {
//	    AppName *bear.Value `prefix:"app.name"`
//	    Port    *bear.Value `prefix:"server.port"`
//	    Enable  *bear.Value `prefix:"feature.enabled"`
//	}
//
//	支持 int, string, bool, float64 等基础类型
type Value struct {
	prefix string
	key    string
	value  interface{}
}

// NewValue 创建一个新的 Value 实例
func NewValue(prefix, key string) *Value {
	return &Value{
		prefix: prefix,
		key:    key,
	}
}

// GetString 获取字符串值
func (v *Value) GetString() string {
	if v == nil || v.value == nil {
		return ""
	}
	if s, ok := v.value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v.value)
}

// GetInt 获取整数值
func (v *Value) GetInt() int {
	if v == nil || v.value == nil {
		return 0
	}
	switch val := v.value.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case int32:
		return int(val)
	case float64:
		return int(val)
	}
	return 0
}

// GetInt64 获取 int64 值
func (v *Value) GetInt64() int64 {
	if v == nil || v.value == nil {
		return 0
	}
	switch val := v.value.(type) {
	case int:
		return int64(val)
	case int64:
		return val
	case int32:
		return int64(val)
	case float64:
		return int64(val)
	}
	return 0
}

// GetFloat 获取浮点数值
func (v *Value) GetFloat() float64 {
	if v == nil || v.value == nil {
		return 0
	}
	if f, ok := v.value.(float64); ok {
		return f
	}
	return 0
}

// GetBool 获取布尔值
func (v *Value) GetBool() bool {
	if v == nil || v.value == nil {
		return false
	}
	if b, ok := v.value.(bool); ok {
		return b
	}
	return false
}

// String 实现 fmt.Stringer 接口
func (v *Value) String() string {
	return v.GetString()
}

// getConfigValue 从 SysConfig 中获取配置值
// 支持通过点号路径访问嵌套配置，如 "server.port" 或 "app.name"
func getConfigValue(config *SysConfig, path string) interface{} {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil
	}

	// 从 Config map 中获取
	if config.Config != nil {
		if val, ok := config.Config[path]; ok {
			return val
		}
	}

	return nil
}
