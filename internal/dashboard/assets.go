package dashboard

import "embed"

// Dist contains the production dashboard SPA. The checked-in placeholder keeps
// the Go binary buildable before the React app is built.
//
//go:embed dist/*
var Dist embed.FS
