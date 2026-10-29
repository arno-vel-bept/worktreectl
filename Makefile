.PHONY: build test check snapshot

build:
	go build -trimpath -o bin/worktreectl ./cmd/worktreectl

test:
	go test -race ./...

check:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)"
	shellcheck install.sh
	python3 -m unittest discover -s tests -p 'test_*.py'

snapshot:
	goreleaser release --snapshot --clean
