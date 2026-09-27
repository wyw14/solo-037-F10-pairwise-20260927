package model

import "time"

type EvidenceKind string

const (
    EvidenceWaveTime EvidenceKind = "wave_time"
    EvidenceWaveSpectrum EvidenceKind = "wave_spectrum"
    EvidenceForce EvidenceKind = "force"
    EvidencePosition EvidenceKind = "position"
    EvidenceBrake EvidenceKind = "brake"
)

type Evidence struct {
    ID EventID `json:"id"`
    Run RunID `json:"run"`
    Revision RevisionID `json:"revision"`
    SourceID string `json:"source_id"`
    Kind EvidenceKind `json:"kind"`
    Value float64 `json:"value"`
    RecordedAt time.Time `json:"recorded_at"`
    Durable bool `json:"durable"`
    // FrameID is the client-supplied idempotency key for one reading. It is
    // empty for evidence that did not arrive through the entry console.
    FrameID string `json:"frame_id,omitempty"`
}

type DeviceCommand struct {
    ID string `json:"id"`
    Run RunID `json:"run"`
    Epoch uint64 `json:"epoch"`
    Fence uint64 `json:"fence"`
    Setpoint float64 `json:"setpoint"`
    IssuedAt time.Time `json:"issued_at"`
}

type DeviceReceipt struct {
    CommandID string `json:"command_id"`
    Epoch uint64 `json:"epoch"`
    Velocity float64 `json:"velocity"`
    Confirmed bool `json:"confirmed"`
    RecordedAt time.Time `json:"recorded_at"`
}

func NewEvidence(run RunID, revision RevisionID, source string, kind EvidenceKind, value float64, at time.Time) Evidence {
    return Evidence{
        ID: NewEventID(),
        Run: run,
        Revision: revision,
        SourceID: source,
        Kind: kind,
        Value: value,
        RecordedAt: at.UTC(),
    }
}

func (e Evidence) WithDurability(durable bool) Evidence {
    e.Durable = durable
    return e
}

func (e Evidence) WithFrameID(frame string) Evidence {
    e.FrameID = frame
    return e
}

func (e Evidence) SamePhysicalSource(other Evidence) bool {
    return e.Run == other.Run && e.Revision == other.Revision && e.SourceID == other.SourceID
}

// SameFramePayload reports whether two frames carry the same physical reading,
// which is what makes a retransmitted frame a harmless retry rather than a
// conflict.
func (e Evidence) SameFramePayload(other Evidence) bool {
    return e.Kind == other.Kind && e.SourceID == other.SourceID && e.Value == other.Value && e.RecordedAt.Equal(other.RecordedAt)
}
