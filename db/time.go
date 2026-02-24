package db

import "time"

// timeNow is a function that returns the current time.
// It can be overridden in tests for deterministic behavior.
var timeNow = time.Now
