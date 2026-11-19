// Package speech gives an agent's replies a sound and the user's speech words:
// the audio connection, text-to-speech and speech-to-text around conversation,
// which stays text only. Its core declares a port for each speech stage; vendor
// packages are imported only by the adapters that implement them, never here.
package speech
