// Package daemon owns DARKSTAR daemon lifecycle and application coordination.
//
// Runtime behavior is introduced by the daemon-foundation stories. Keeping the
// package independent from terminal and web transports lets both clients use the
// same application behavior.
package daemon
