package core

import "errors"

// ErrVerifyFailed marks a completed verify that found problems.
var ErrVerifyFailed = errors.New("verification failed")

// ErrUpdateAvailable is `update -check` finding a newer release: exit 2, with
// the message already printed.
var ErrUpdateAvailable = errors.New("an update is available")
