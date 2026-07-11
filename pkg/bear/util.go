package bear

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v2"
)

func stringSlicePointer(values ...string) *[]string {
	copyOfValues := cloneStringSlice(values)
	return &copyOfValues
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

// Validator 接口允许结构体执行自定义校验逻辑 (如跨字段校验)
type Validator interface {
	Validate(ctx *gin.Context) error
}

// ParseConfig 自动根据文件扩展名解析配置文件 (支持 JSON 和 YAML)
func ParseConfig(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, out); err != nil {
			return fmt.Errorf("failed to parse YAML config (%s): %w", path, err)
		}
		return nil
	case ".json":
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("failed to parse JSON config (%s): %w", path, err)
		}
		return nil
	default:
		// 如果没有扩展名，根据内容特征探测：YAML 通常以字母开头（如 auth:），JSON 通常以 { 或 [ 开头
		trimmed := strings.TrimSpace(string(data))
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return json.Unmarshal(data, out)
		}
		return yaml.Unmarshal(data, out)
	}
}

// GetAbsPath 返回路径的绝对路径。
// 增强逻辑：优先尝试 CWD，若找不到则尝试相对于二进制执行文件所在的目录。
func GetAbsPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	// 1. 尝试相对于当前工作目录 (CWD)
	abs, err := filepath.Abs(path)
	if err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}

	// 2. 尝试相对于程序执行路径 (Executable Path)
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		// 排除 go run 产生的临时构建目录干扰
		if !strings.Contains(exeDir, "go-build") && !strings.Contains(exeDir, "/T/") {
			target := filepath.Join(exeDir, path)
			if _, err := os.Stat(target); err == nil {
				return target
			}
		}
	}

	return abs // 最终回退到最初的 CWD 绝对路径
}

// JoinRoot 将路径片段连接并转换为绝对路径
func JoinRoot(elem ...string) string {
	return GetAbsPath(filepath.Join(elem...))
}

// WriteFileAtomic 原子性地写入文件：先写临时文件再重命名，防止崩溃导致数据损坏
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, "bear-tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
