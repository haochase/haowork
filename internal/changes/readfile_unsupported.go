//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !zos

package changes

import "errors"

func readFileNoFollow(string) ([]byte, error) {
	return nil, errors.New("secure no-follow reads are unsupported on this platform")
}
