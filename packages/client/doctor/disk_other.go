//go:build !unix

package doctor

import "errors"

func diskFree(string) (uint64, error) {
	return 0, errors.New("not measured on this platform yet")
}
