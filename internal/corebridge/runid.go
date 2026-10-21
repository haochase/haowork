package corebridge

import "errors"

var ErrInvalidRunID = errors.New("Core Bridge RunID is invalid")

const maxRunIDLength = 128

// ValidateRunID accepts one stable ASCII URL path segment without relying on
// escaping or trimming to change caller input.
func ValidateRunID(value string) error {
	if len(value) == 0 || len(value) > maxRunIDLength {
		return ErrInvalidRunID
	}
	for index, character := range []byte(value) {
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if alphaNumeric {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.' || character == ':') {
			continue
		}
		return ErrInvalidRunID
	}
	return nil
}
