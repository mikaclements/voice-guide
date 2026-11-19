// Package conversation runs sessions and their turns: it builds each turn's
// prompt, asks the model, streams the reply and records what was said. It
// works in text only, so typed words and spoken ones take the same path; the
// speech module turns audio into words and replies into audio around it.
// Vendor packages are imported only by adapters, never here, and user context
// is reached through its service.
package conversation
