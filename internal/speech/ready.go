package speech

// answerWarm tells the page the session can take the user's words, once per
// connection, the page having asked as its data channel opened, since voice
// can't send on the channel before then. A page that asks twice is told once.
func (r *replies) answerWarm() {
	if r.toldReady {
		return
	}
	r.toldReady = true
	r.s.tell(r.conn, out{Type: "ready"})
}
