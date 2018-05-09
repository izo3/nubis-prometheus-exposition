# Makefile for go project

NAME=nubis-prometheus-exposition
GO15VENDOREXPERIMENT := 1
VERSION=$(shell git describe --always --tags --dirty)
FLAGS=-X main.Version=$(VERSION)

build:
	rm -rf build && mkdir -p build
	mkdir -p build/linux && GOOS=linux go build -ldflags="$(FLAGS)" -o build/linux/$(NAME) ./*.go
	mkdir -p build/darwin && GOOS=darwin go build -ldflags="$(FLAGS)" -o build/darwin/$(NAME) ./*.go

fmt:
	gofmt -w=true $$(find . -type f -name '*.go')

clean:
	rm -rf build

all: clean build

.PHONY: build fmt
