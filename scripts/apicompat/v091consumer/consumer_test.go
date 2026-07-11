package v091consumer_test

import (
	"context"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear/gen"
)

type legacyService struct{}

func (*legacyService) Name() string { return "legacyService" }

type legacyController struct{}

func (*legacyController) Name() string                 { return "legacyController" }
func (*legacyController) Build(*bear.Bear)             {}
func (*legacyController) Interceptors() []bear.Fairing { return nil }

func compileV091Consumer() {
	var setLogger func() = bear.SetDefaultLogger
	setLogger()
	var authLeft, authRight bear.AuthConfig
	var websocketLeft, websocketRight bear.WebSocketConfig
	_ = authLeft == authRight
	_ = websocketLeft == websocketRight

	config := bear.NewSysConfig()
	app := bear.Ignite(config)
	app.Beans(&legacyService{})
	app.Mount("/api", &legacyController{})
	app.Attach(&bear.BaseFairing{})
	app.Handle("GET", "/legacy", func() bear.Response { return bear.Success("ok") })
	_ = app.ApplyAll(context.Background())
	_ = gen.NewGenerator("legacy")
}

var _ = compileV091Consumer
