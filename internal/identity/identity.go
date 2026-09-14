// Package identity models the enrollment identity of an edge agent.
//
// This milestone only provides the abstraction: there is no real enrollment,
// no tokens, no certificates and no SaaS API call.
package identity

// EnrollmentStatus is the conceptual enrollment state of this agent.
type EnrollmentStatus string

const (
	// StatusUnenrolled means no edge ID has been assigned yet.
	StatusUnenrolled EnrollmentStatus = "UNENROLLED"
	// StatusEnrolled means an edge ID is present.
	StatusEnrolled EnrollmentStatus = "ENROLLED"
)

func (s EnrollmentStatus) String() string { return string(s) }

// Identity is the agent's identity snapshot.
type Identity struct {
	EdgeID string
	Status EnrollmentStatus
}

// New derives an Identity from the configured edge ID.
func New(edgeID string) Identity {
	if edgeID == "" {
		return Identity{Status: StatusUnenrolled}
	}
	return Identity{EdgeID: edgeID, Status: StatusEnrolled}
}

// IsEnrolled reports whether the agent has a conceptual identity.
func (i Identity) IsEnrolled() bool { return i.Status == StatusEnrolled }
