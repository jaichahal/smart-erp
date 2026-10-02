package identity

// deviceRegisteredArgs is the outbox job emitted when a device public key is stored.
type deviceRegisteredArgs struct {
	DeviceID string `json:"device_id"`
	JKT      string `json:"jkt"`
	Platform string `json:"platform"`
}

// Kind is the River job kind.
func (deviceRegisteredArgs) Kind() string { return "device.registered" }

// sessionStartedArgs is emitted in the same transaction as the new session.
type sessionStartedArgs struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	DeviceID  string `json:"device_id"`
}

// Kind is the River job kind.
func (sessionStartedArgs) Kind() string { return "session.started" }

// sessionEndedArgs is emitted when a session is ended, including cap eviction.
type sessionEndedArgs struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

// Kind is the River job kind.
func (sessionEndedArgs) Kind() string { return "session.ended" }
