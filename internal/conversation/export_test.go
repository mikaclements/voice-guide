package conversation

import "time"

// OpeningCue is what the model is sent in the user's place when the agent opens a session.
const OpeningCue = openingCue

// SetClock has s time a turn's stages by now, so a test steps the clock and
// checks each stage's time exactly.
func SetClock(s *Service, now func() time.Time) { s.now = now }

// SetDeliveriesClock has d time and sign each attempt by now, so a test
// checks a delivery's timestamp and signature exactly.
func SetDeliveriesClock(d *Deliveries, now func() time.Time) { d.now = now }
