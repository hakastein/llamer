.PHONY: go build dist

TARGETS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

go:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go test $(TESTFLAGS) ./...

build:
	CGO_ENABLED=0 go build -o bin/ .

# dist собирает бинарники релиза dist/llamer-<os>-<arch> и dist/sha256sums.txt.
dist:
	rm -rf dist && mkdir dist
	for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags=-s -o dist/llamer-$$os-$$arch . || exit 1; \
	done
	cd dist && sha256sum llamer-* > sha256sums.txt
