package core

import "errors"

// ErrVerifyFailed marks a completed verify that found problems.
var ErrVerifyFailed = errors.New("verification failed")
