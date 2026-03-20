package bear

import (
	"fmt"
	"net/http"
	"os"
	"sync"

	"gopkg.in/yaml.v2"
)

// BearError 定义业务错误
type BearError struct {
	Code    int           `json:"code"`    // 业务状态码
	Status  int           `json:"-"`       // 对应 HTTP 状态码
	Message string        `json:"message"` // 错误消息 (默认或翻译后)
	Key     string        `json:"-"`       // I18n 翻译键
	Args    []interface{} `json:"-"`       // 翻译参数
	Err     error         `json:"-"`       // 底层原始错误 (用于 errors.Unwrap)
}

func (e *BearError) Error() string {
	msg := ""
	if e.Key != "" {
		msg = fmt.Sprintf("Error %d (key: %s)", e.Code, e.Key)
	} else {
		msg = fmt.Sprintf("Error %d: %s", e.Code, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", msg, e.Err)
	}
	return msg
}

// Unwrap 实现 Go 1.13+ 错误包装接口
func (e *BearError) Unwrap() error {
	return e.Err
}

// Is 实现语义判定接口
func (e *BearError) Is(target error) bool {
	t, ok := target.(*BearError)
	if !ok {
		return false
	}
	// 只要业务码一致，即视为同类错误
	return e.Code == t.Code
}

// WithMsg 设置自定义消息并返回副本
func (e *BearError) WithMsg(msg string) *BearError {
	newErr := *e
	newErr.Message = msg
	return &newErr
}

// WithErr 包装底层错误并返回副本
func (e *BearError) WithErr(err error) *BearError {
	newErr := *e // 浅拷贝
	newErr.Err = err
	return &newErr
}

// WithArgs 设置翻译参数并返回副本
func (e *BearError) WithArgs(args ...interface{}) *BearError {
	newErr := *e
	newErr.Args = args
	return &newErr
}

// ErrorRegistry 错误码注册表
type ErrorRegistry struct {
	mu     sync.RWMutex
	errors map[int]*BearError
}

var defaultRegistry = &ErrorRegistry{
	errors: make(map[int]*BearError),
}

// RegisterError 注册一个全局错误码
func RegisterError(code int, status int, key string, defaultMsg string) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	defaultRegistry.errors[code] = &BearError{
		Code:    code,
		Status:  status,
		Key:     key,
		Message: defaultMsg,
	}
}

// GetError 从注册表中获取错误实例
func GetError(code int, args ...interface{}) *BearError {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	if e, ok := defaultRegistry.errors[code]; ok {
		// 返回副本，避免修改原始定义
		return &BearError{
			Code:    e.Code,
			Status:  e.Status,
			Key:     e.Key,
			Message: e.Message,
			Args:    args,
		}
	}
	// 如果没找到，返回通用错误
	return &BearError{Code: code, Status: http.StatusInternalServerError, Message: "Unknown Error", Args: args}
}

// ErrorDefinition 用于 YAML 解析的结构
type ErrorDefinition struct {
	Code    int    `yaml:"code"`    // 业务错误码
	Status  int    `yaml:"status"`  // HTTP 状态码
	Key     string `yaml:"key"`     // I18n 翻译键
	Message string `yaml:"message"` // 默认描述
}

// LoadErrorsFromYAML 从指定路径加载错误码定义
func (r *ErrorRegistry) LoadErrorsFromYAML(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var defs []ErrorDefinition
	if err := yaml.Unmarshal(data, &defs); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		r.errors[d.Code] = &BearError{
			Code:    d.Code,
			Status:  d.Status,
			Key:     d.Key,
			Message: d.Message,
		}
	}
	Info("Loaded business error codes from YAML", "path", path, "count", len(defs))
	return nil
}

// GetDefaultRegistry 获取默认注册表
func GetDefaultRegistry() *ErrorRegistry {
	return defaultRegistry
}

// NewError 创建自定义业务错误
func NewError(code int, key string, args ...interface{}) *BearError {
	return &BearError{Code: code, Key: key, Args: args}
}

// ToResponse 将业务错误转换为标准响应
func (e *BearError) ToResponse() Response {
	return Response{
		Code:    e.Code,
		Message: e.Message,
	}
}

// 初始化预定义错误
func init() {
	RegisterError(http.StatusNotFound, http.StatusNotFound, "error_not_found", "Resource not found")
	RegisterError(http.StatusBadRequest, http.StatusBadRequest, "error_invalid_params", "Invalid parameters")
	RegisterError(http.StatusUnauthorized, http.StatusUnauthorized, "error_unauthorized", "Unauthorized")
	RegisterError(http.StatusInternalServerError, http.StatusInternalServerError, "error_internal", "Internal server error")
}

// 预定义错误快捷方式 (向后兼容)
var (
	ErrNotFound       = GetError(http.StatusNotFound)
	ErrInvalidParams  = GetError(http.StatusBadRequest)
	ErrBadRequest     = GetError(http.StatusBadRequest)
	ErrUnauthorized   = GetError(http.StatusUnauthorized)
	ErrForbidden      = GetError(http.StatusForbidden)
	ErrInternalServer = GetError(http.StatusInternalServerError)
)
