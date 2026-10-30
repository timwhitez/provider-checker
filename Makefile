.PHONY: test build build-windows build-macos run-macos vet clean

test:
	go test ./...

vet:
	go vet ./...

# Windows GUI executable (cross-compiles from any OS).
build: build-windows

build-windows:
	./build.sh

# macOS app bundle (must run on macOS with the Xcode Command Line Tools).
build-macos:
	./build_macos.sh

run-macos: build-macos
	open "dist/Provider Checker.app"

clean:
	rm -rf dist
	rm -f provider-checker.exe /tmp/provider-checker-test.exe
