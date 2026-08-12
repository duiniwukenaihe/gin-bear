.PHONY: all test run swagger verify verify-rc

all: test run

test:
	go test ./...

verify:
	scripts/ci-diagnostic.sh "Quality baseline failed" scripts/release-check.sh

verify-rc:
	scripts/verify-rc.sh

run:
	go run cmd/main.go

swagger:
	swag init --parseDependency --parseInternal -g pkg/bear/bear.go
