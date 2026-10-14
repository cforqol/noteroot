#!/bin/bash
# install_go_dev.sh
# 

go install github.com/air-verse/air@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

air -v && staticcheck -version && govulncheck -version
