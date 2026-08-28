default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	go generate ./... && cd tools; go generate ./...

check-api-specs:
	./scripts/check-api-specs.sh

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

testacc:
	TF_ACC=1 go test -v -cover $(TESTARGS) -timeout 120m ./...

.PHONY: fmt lint test testacc build install generate check-api-specs
