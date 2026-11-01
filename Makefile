.PHONY: all check fmt-check vet test build

all: check build

# What must pass before every commit
check: fmt-check vet test

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -o bin/peerglass ./cmd/peerglass
