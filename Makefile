.PHONY: build test lint chart-check ci

build:
	go build ./...

test:
	go test -race ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l cmd internal)"
	golangci-lint run ./...

chart-check:
	helm lint charts/vsphere-proxy
	./charts/vsphere-proxy/tests/render.sh

ci: lint test build chart-check
	go mod verify
