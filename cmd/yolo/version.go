// Version injection (Sprint 11 H-008). `version` is set at link time via
// -ldflags "-X main.version=<value>" so released binaries report something
// other than the dev default. The value is accessed by `yolo --version`.

package main

// version is injected by the release build pipeline. The default keeps local
// dev builds honest.
var version = "dev"
