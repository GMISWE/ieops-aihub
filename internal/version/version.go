package version

import "time"

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// ProcessStartTime is when THIS process started, captured once at package
// initialisation and never reassigned.
//
// 🔴 It exists because the other three values above cannot answer the question
// GET /v1/version is now asked (aihub#416 D5): all three come from build-time
// ldflags, so they are byte-identical before and after a restart of the same
// image. An observer that reads a service's "generation" at the start of a long
// observation and re-reads it at the end needs a value that CHANGES when the
// thing it observed was replaced — and a restart replaces it just as thoroughly
// as a redeploy does. Without a process component the probe reports "unchanged"
// across a restart, which is a FALSE GREEN: the most dangerous direction for a
// check whose whole job is to invalidate conclusions.
//
// 🔴 And it is why `Version` must NOT be part of a generation expression.
// Measured on this repo's own CI: the main-branch image build passes only
// GIT_COMMIT and BUILD_TIME as build-args, so Dockerfile's `ARG VERSION=dev`
// stands and this field is the constant "dev" in production. Only a tag-driven
// release build sets it. A constant inside a generation string contributes
// nothing but confidence.
//
// Set in an initialiser rather than from main so that every binary in this
// module — and every test that starts the server in-process — gets a value
// without remembering to. There is no setter: a mutable "when did I start"
// invites a caller to make two processes look like one.
//
// ⚠️ NOT a lease, a heartbeat or a liveness signal. Nothing expires on it and no
// code may branch on how large it is; design v1.21 removed that whole family and
// handleRenewLease still answers 410 Gone. It is an identity token that happens
// to be readable as a timestamp.
var ProcessStartTime = time.Now().UTC()
