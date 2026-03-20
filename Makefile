.PHONY: all test run swagger

all: test run

test:
	go test ./...

run:
	go run cmd/main.go

swagger:
	swag init --parseDependency --parseInternal -g pkg/bear/bear.go
