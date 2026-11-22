package core

import "errors"

// ErrVerifyFailed marks a completed verify that found problems.
var ErrVerifyFailed = errors.New("verification failed")

// ErrUpdateAvailable is `update -check` finding a newer release, with the
// message already printed. It exits ExitUpdateAvailable.
var ErrUpdateAvailable = errors.New("an update is available")

// ExitUpdateAvailable is not 2, which a usage error or a mistyped flag exits
// with: a script running `update -chek` must not read "update available".
const ExitUpdateAvailable = 10
