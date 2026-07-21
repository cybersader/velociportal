.PHONY: build test vet check clean

build:
	go build -o velociportal .

test:
	go test -v -race -count=1 ./...

vet:
	go vet ./...

check: vet test

clean:
	rm -f velociportal
