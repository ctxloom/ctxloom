package isolation

import "github.com/ctxloom/ctxloom/internal/shared/platform"

// hostOS is the platform behaviour this package reaches the OS through
// (platform.Current), a package var so a test can stand in another
// platform's answers.
var hostOS platform.Host = platform.Current()
