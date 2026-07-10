package bear

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v2"
)

const LocalizerKey = "bear_localizer"

// I18nManager 管理消息包
type I18nManager struct {
	Bundle *i18n.Bundle
	Config *I18nConfig
}

func (i *I18nManager) Name() string {
	return "I18nManager"
}

func NewI18nManager(cfg *I18nConfig) *I18nManager {
	bundle := i18n.NewBundle(language.MustParse(cfg.DefaultLanguage))
	bundle.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)

	// 加载翻译文件
	files, err := os.ReadDir(cfg.BundlePath)
	if err != nil {
		slog.Warn("Failed to read i18n bundle path", "path", cfg.BundlePath, "error", err)
	} else {
		for _, f := range files {
			if !f.IsDir() && filepath.Ext(f.Name()) == "."+cfg.Format {
				path := filepath.Join(cfg.BundlePath, f.Name())
				bundle.MustLoadMessageFile(path)
				slog.Info("Loaded i18n message file", "path", path)
			}
		}
	}

	return &I18nManager{Bundle: bundle, Config: cfg}
}

func (i *I18nManager) GetLocalizer(langs ...string) *i18n.Localizer {
	return i18n.NewLocalizer(i.Bundle, langs...)
}

// I18nFairing 处理语言检测的中间件
type I18nFairing struct {
	BaseFairing
}

func (i *I18nFairing) Name() string {
	return "I18nFairing"
}

func (i *I18nFairing) OnRequest(ctx *gin.Context) error {
	manager := GetByType[*I18nManager]()
	if manager == nil {
		return nil
	}

	// 1. 检测逻辑：Query -> Cookie -> Header -> Default
	lang := ctx.Query("lang")
	if lang == "" {
		lang, _ = ctx.Cookie("lang")
	}
	if lang == "" {
		lang = ctx.GetHeader("Accept-Language")
	}

	localizer := manager.GetLocalizer(lang, manager.Config.DefaultLanguage)
	ctx.Set(LocalizerKey, localizer)
	return nil
}

// GetLocalizer 从 Context 中获取 Localizer
func GetLocalizer(ctx *gin.Context) *i18n.Localizer {
	if val, ok := ctx.Get(LocalizerKey); ok {
		return val.(*i18n.Localizer)
	}
	return nil
}
