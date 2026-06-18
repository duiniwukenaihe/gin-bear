package gen

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
