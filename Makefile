LDFLAGS := -s -w

.PHONY: build linux demo test clean

build:
	go build -ldflags="$(LDFLAGS)" -o tns .

linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/tns-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/tns-linux-arm64 .
	cp scripts/install.sh dist/

demo: build
	./tns -demo

test:
	go test ./...

clean:
	rm -rf tns dist
