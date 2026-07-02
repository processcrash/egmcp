# Placeholder directory — the production build replaces the contents
# of internal/server/assets with the contents of web/dist at build time
# via a Dockerfile step.
#
# This README exists only so `go:embed` has a file to grab when the
# frontend has not been built. The server's staticHandler falls back to
# an HTML placeholder when index.html is missing.
