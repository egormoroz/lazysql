.PHONY: build lint vet test clean

build:
	go build -o pgviewer .

lint:
	golangci-lint run

vet:
	go vet ./...

test:
	go test ./...

check: vet lint

clean:
	rm -f pgviewer
