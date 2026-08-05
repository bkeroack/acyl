.PHONY: build generate test

default: build

# The 'safe' tag is required at runtime; see "Building from source" in README.md.
build:
	go build -mod=vendor -tags safe -o acyl .

generate:
	go generate ./...

check:
	./check.sh

docs:
	./openapi.sh
