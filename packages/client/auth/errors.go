package auth

import (
	"errors"

	"github.com/xlsft/harness/packages/client/llm"
)

func isAuthErr(err error) bool {
	var ae *llm.APIError
	return errors.As(err, &ae) && ae.Type == "authentication"
}
