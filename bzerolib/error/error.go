package error

const SchemaVersion = "042022"

type ErrorType string

const (
	// this is any error with the validation of the message itself
	// e.g. invalid signature, expired bzcert, wrong hpointer, etc.
	// The responding actions of any given error type should be the same
	KeysplittingValidationError ErrorType = "KeysplittingValidationError"

	// Components such as datachannel, plugin, actions report their actions here.
	// Theoretically, there should be two kinds: any errors that come from
	// startup and any error independent of the message that arises during regular
	// functioning.
	ComponentStartupError    ErrorType = "ComponentStartupError"
	ComponentProcessingError ErrorType = "ComponentProcessingError"
)

type ErrorMessage struct {
	SchemaVersion string `json:"schemaVersion" default:"042022"`
	Timestamp     int64  `json:"timestamp"`
	Type          string `json:"type"`
	Message       string `json:"message"`
	HPointer      string `json:"hPointer"`
}
