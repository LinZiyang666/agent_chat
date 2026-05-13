// Package api wires the daemon's HTTP server: routes, handlers, and
// middleware. Handlers depend on service interfaces from sibling business
// packages; they MUST NOT perform direct I/O against the store or any bot
// implementation. See docs/03-architecture.md §8.
package api
