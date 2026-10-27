// Package service installs Laranest as a per-user background service: a
// systemd user unit on Linux; on Windows, the binary under
// %LOCALAPPDATA%\Programs\Laranest started at sign-in by a windowless
// launcher (no administrator rights in either case).
package service
