package bear

// Response 标准 API 返回结构
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Result 快捷创建成功响应
func Result(code int, msg string, data interface{}) Response {
	return Response{
		Code:    code,
		Message: msg,
		Data:    data,
	}
}

// Success 快捷返回成功
func Success(data interface{}) Response {
	return Result(200, "success", data)
}

// Error 快捷返回错误
func Error(code int, msg string) Response {
	return Result(code, msg, nil)
}
