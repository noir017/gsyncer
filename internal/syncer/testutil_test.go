package syncer

import "fmt"

func fmtSprintfImpl(f string, a []any) string { return fmt.Sprintf(f, a...) }
