package syncer

import (
	"errors"
	"fmt"
	"os"

	"gsync/internal/execx"
)

func fmtSprintfImpl(f string, a []any) string { return fmt.Sprintf(f, a...) }

// cpHardlinkFake emulates `cp` on a non-CoW filesystem for syncer tests: the
// hardlink backend's reflink probe (`cp --reflink=always <src> <dst>`) fails, so
// Create takes the `cp -al <cur> <tmp>` path, which we emulate by creating the
// destination directory (always the last arg) so the temp+rename completes.
func cpHardlinkFake(args []string) (execx.Result, error) {
	if len(args) > 0 && args[0] == "--reflink=always" {
		return execx.Result{Code: 1}, errors.New("reflink not supported")
	}
	_ = os.MkdirAll(args[len(args)-1], 0o755)
	return execx.Result{}, nil
}
