.PHONY: build run clean

build:
	go build -o server ./cmd/server

run: build
	./server

clean:
	rm -f server
