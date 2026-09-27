package model

import "time"

// OperationOrigin separates the fixed diagnostic drill scenario from
// operator-supplied measurements so pages and archives never mix them.
type OperationOrigin string

const (
    OriginDiagnostic OperationOrigin = "diagnostic"
    OriginMeasured   OperationOrigin = "measured"
)

type Operation struct {
    ID RunID `json:"id"`
    Name string `json:"name"`
    ModelName string `json:"model_name"`
    Section string `json:"section"`
    Carriage string `json:"carriage"`
    Stage RunStage `json:"stage"`
    Revision RevisionID `json:"revision"`
    Origin OperationOrigin `json:"origin"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

func NewOperation(name string, modelName string, section string, carriage string, at time.Time) Operation {
    now := at.UTC()
    return Operation{
        ID: NewRunID(),
        Name: name,
        ModelName: modelName,
        Section: section,
        Carriage: carriage,
        Stage: StageConfigured,
        Revision: NewRevisionID(),
        Origin: OriginDiagnostic,
        CreatedAt: now,
        UpdatedAt: now,
    }
}

func (o Operation) Advance(to RunStage, at time.Time) (Operation, error) {
    stage, err := Advance(o.Stage, to)
    if err != nil {
        return o, err
    }
    o.Stage = stage
    o.UpdatedAt = at.UTC()
    return o, nil
}

func (o Operation) Abort(at time.Time) Operation {
    if !Terminal(o.Stage) {
        o.Stage = StageAborted
        o.UpdatedAt = at.UTC()
    }
    return o
}

func (o Operation) Active() bool {
    return !Terminal(o.Stage)
}

type Incident struct {
    ID EventID `json:"id"`
    Run RunID `json:"run"`
    Severity string `json:"severity"`
    Message string `json:"message"`
    CreatedAt time.Time `json:"created_at"`
}

func NewIncident(run RunID, severity string, message string, at time.Time) Incident {
    return Incident{ID: NewEventID(), Run: run, Severity: severity, Message: message, CreatedAt: at.UTC()}
}
