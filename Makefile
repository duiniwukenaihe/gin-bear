.PHONY: all test run swagger verify

all: test run

test:
	go test ./...

verify:
	scripts/release-check.sh

run:
	go run cmd/main.go

swagger:
	swag init --parseDependency --parseInternal -g pkg/bear/bear.go
