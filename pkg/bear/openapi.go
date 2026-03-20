package bear

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
)

// RouteMetadata 记录路由元数据用于生成文档
type RouteMetadata struct {
	Method      string
	Path        string
	GroupName   string
	HandlerType reflect.Type
	HandlerName string
}

// OpenAPIInfo 接口描述元数据
type OpenAPIInfo struct {
	Summary     string
	Description string
	Tags        []string
}

// IOpenAPI 控制器可实现此接口以提供更多文档信息
type IOpenAPI interface {
	OpenAPI() map[string]OpenAPIInfo // path -> info
}

// OpenAPISchema 简化的 OpenAPI 3.0 定义结构
type OpenAPISchema struct {
	OpenAPI    string                 `json:"openapi"`
	Info       map[string]interface{} `json:"info"`
	Paths      map[string]interface{} `json:"paths"`
	Components map[string]interface{} `json:"components"`
}

// GenerateOpenAPI 生成 OpenAPI 3.0 文档内容
func (this *Bear) GenerateOpenAPI() ([]byte, error) {
	config := GetByType[*SysConfig]()
	schema := OpenAPISchema{
		OpenAPI: "3.0.0",
		Info: map[string]interface{}{
			"title":   config.Server.Name,
			"version": "1.0.0",
		},
		Paths: make(map[string]interface{}),
		Components: map[string]interface{}{
			"schemas": make(map[string]interface{}),
		},
	}

	// 遍历路由元数据
	for _, route := range this.routeRegistry {
		path := route.Path
		if route.GroupName != "" && !strings.HasPrefix(path, "/"+route.GroupName) {
			path = "/" + route.GroupName + path
		}
		// 统一路径格式
		path = strings.ReplaceAll(path, "//", "/")

		if _, exists := schema.Paths[path]; !exists {
			schema.Paths[path] = make(map[string]interface{})
		}

		op := map[string]interface{}{
			"operationId": route.HandlerName,
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "OK",
				},
			},
		}

		// 检查控制器是否提供了额外元数据
		if bean := GetInjector().Get(route.HandlerType); bean != nil {
			if ctrl, ok := bean.(IOpenAPI); ok {
				if info, exists := ctrl.OpenAPI()[route.Path]; exists {
					op["summary"] = info.Summary
					op["description"] = info.Description
					op["tags"] = info.Tags
				}
			}
		}

		schema.Paths[path].(map[string]interface{})[strings.ToLower(route.Method)] = op
	}

	return json.MarshalIndent(schema, "", "  ")
}

// EnableSwagger 启用 Swagger 文档支持
func (this *Bear) EnableSwagger() *Bear {
	swg := this.Group("/swagger")

	// 注册 JSON 文档端点
	swg.GET("/doc.json", func(c *gin.Context) {
		doc, err := this.GenerateOpenAPI()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.Data(http.StatusOK, "application/json", doc)
	})

	// 简单的 Swagger UI 代理或重定向提示
	swg.GET("", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Swagger UI</title>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@3/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@3/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/swagger/doc.json',
        dom_id: '#swagger-ui',
      });
    };
  </script>
</body>
</html>`)
	})

	return this
}
