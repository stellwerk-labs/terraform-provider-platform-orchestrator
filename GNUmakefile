default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	go generate ./...
	cd tools && go generate ./...

check-api-specs:
	./scripts/check-api-specs.sh

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

test-native-runner-cli:
	@set -eu; \
	for tool in go terraform tofu; do \
		command -v "$$tool" >/dev/null || { printf '%s is required\n' "$$tool" >&2; exit 1; }; \
	done; \
	tests="$$(go test -list '^TestNativeAgentRunnerActualCLI$$' ./internal/provider)"; \
	printf '%s\n' "$$tests" | grep -qx 'TestNativeAgentRunnerActualCLI'; \
	scratch="$$(mktemp -d "$${TMPDIR:-/tmp}/stellwerk-native-provider.XXXXXX")"; \
	cleanup() { \
		exit_status=$$?; trap - EXIT INT TERM; \
		rm -rf "$$scratch" || exit_status=1; \
		exit "$$exit_status"; \
	}; \
	trap cleanup EXIT; \
	trap 'exit 130' INT; trap 'exit 143' TERM; \
	scratch="$$(cd "$$scratch" && pwd -P)"; \
	candidate="$$scratch/terraform-provider-platform-orchestrator"; \
	go build -o "$$candidate" .; \
	NATIVE_RUNNER_PROVIDER_BINARY="$$candidate" go test -race -v -count=1 -timeout=120s \
		./internal/provider -run '^TestNativeAgentRunnerActualCLI$$'

testacc:
	TF_ACC=1 go test -v -cover $(TESTARGS) -timeout 120m ./...

.PHONY: fmt lint test test-native-runner-cli testacc build install generate check-api-specs
