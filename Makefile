.PHONY: web build test linux-amd64 proto node-bundle

# 固定纯 Go 编译器及插件版本，本机和 CI 均不依赖预装 protoc。
proto:
	go run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate api --template buf.gen.yaml

web:
	npm --prefix web ci --no-fund --no-audit
	npm --prefix web run build

node-bundle:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o bin/nbpass-node-linux-amd64 ./cmd/nbpass-node
	gzip -n -c bin/nbpass-node-linux-amd64 > internal/nodebundle/nbpass-node-linux-amd64.gz.tmp
	mv internal/nodebundle/nbpass-node-linux-amd64.gz.tmp internal/nodebundle/nbpass-node-linux-amd64.gz

build: web node-bundle
	go build -trimpath -o bin/nbpass ./cmd/nbpass
	go build -trimpath -o bin/nbpass-node ./cmd/nbpass-node

test: web node-bundle
	go vet ./...
	go test ./...

linux-amd64: web node-bundle
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/nbpass-linux-amd64 ./cmd/nbpass
