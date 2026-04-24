package bootstrap

import "time"

// timeNow is a var so tests can override. We don't need freezing at the
// moment but keeping the indirection is cheap.
var timeNow = time.Now
