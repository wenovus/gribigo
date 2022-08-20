## How to run this script

```bash
// First, clone the lemming repository and go to the sysrib branch: `git branch sysrib`
cd <gribigo repo root>
go mod edit -replace=github.com/openconfig/lemming=$GOPATH/src/github.com/openconfig/lemming
go get ./...
go run ./cmd/rtr/ &
go test cmd/test/inject_test.go -addr 127.0.0.1:9340 -insecure
```
