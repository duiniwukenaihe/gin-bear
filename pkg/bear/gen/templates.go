package gen

// ControllerTemplate renders a fairing-shaped controller that serves one index
// route. It is exported for callers of GenerateFromTemplate; the package's own
// scanner does not consume it.
const ControllerTemplate = `package controllers

import (
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type {{.Name}}Controller struct {
	bear.BaseFairing
}

func New{{.Name}}Controller() *{{.Name}}Controller {
	return &{{.Name}}Controller{}
}

func (this *{{.Name}}Controller) Name() string {
	return "{{.Name}}Controller"
}

func (this *{{.Name}}Controller) Build(b *bear.Bear) {
	b.Handle("GET", "/", this.Index)
}

func (this *{{.Name}}Controller) Index() string {
	return "Hello from {{.Name}}Controller"
}
`

// ServiceTemplate renders a bean-shaped service.
//
// The rendered file imports pkg/bear and references nothing from it, so a service
// package rendered from this constant does not compile until the caller adds code
// that uses bear. That is retained deliberately: the constant's value is part of
// the pinned v0.9.1 public API baseline (scripts/api/v0.9.1.txt), and
// scripts/check-api-compat.sh rejects any value change as a non-additive edit.
// TestExportedServiceTemplateKeepsThePinnedUnusedImport pins the limitation so
// that removing it later is a deliberate baseline update rather than a silent
// drift.
const ServiceTemplate = `package services

import (
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
)

type {{.Name}}Service struct {
}

func New{{.Name}}Service() *{{.Name}}Service {
	return &{{.Name}}Service{}
}

func (this *{{.Name}}Service) Name() string {
	return "{{.Name}}Service"
}
`
