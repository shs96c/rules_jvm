//go:build !linux && !darwin && !windows

package javaparser

import "errors"

func lockCache(string) (func(), bool, error) {
	return nil, false, errors.ErrUnsupported
}
