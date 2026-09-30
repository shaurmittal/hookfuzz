.PHONY: build test e2e demo

build:
	go build -o hookfuzz ./cmd/hookfuzz

test:
	go test ./...
	cd sample-app && npm test

e2e:
	cd sample-app && npm install
	go test -tags e2e ./e2e/ -v

demo: build
	vhs demo.tape
