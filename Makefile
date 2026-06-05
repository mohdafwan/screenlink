GO ?= $(HOME)/.go-sdk/go/bin/go
BIN := bin/screenlink

.PHONY: build run clean

build:
	$(GO) build -o $(BIN) .
	cp capture.py bin/capture.py
	@echo "built $(BIN)"

run: build
	./$(BIN) host --addr :8087 --fps 40 --quality 70

clean:
	rm -rf bin
