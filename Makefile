.PHONY: test build vet clean

test:
	go test ./...

vet:
	go vet ./...

build:
	./build.sh

clean:
	rm -f provider-checker.exe /tmp/provider-checker-test.exe
