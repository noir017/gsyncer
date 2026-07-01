// Package e2e holds gsync's end-to-end tests: they drive the compiled binary
// through its real ssh+rsync pipeline against live servers you supply.
//
// The tests are guarded by the `e2e` build tag (see e2e_test.go), so the default
// `go test ./...` skips them and needs no servers. Run them explicitly:
//
//	cp e2e/e2e.config.example.toml e2e/e2e.config.toml   # then edit in your servers
//	go test -tags e2e ./e2e -v
//
// This file carries no build tag so the package always has a compilable source
// file (a tag-only package would break plain `go build ./...`).
package e2e
