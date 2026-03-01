#!/usr/bin/env bash

export CGO_ENABLED=0
# darwin linux windows
export GOOS=linux
# amd64 arm64
export GOARCH=amd64

go build -o main

scp ./main my:/opt/webapps/dailybot/