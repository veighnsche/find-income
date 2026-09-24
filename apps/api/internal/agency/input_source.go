package agency

import (
	"context"
	"errors"
)

var ErrUnsupportedOwnerSource = errors.New("owner vacancy URL reader unavailable")

type OwnerSourceReader interface {
	ReadVacancy(context.Context, string) (string, error)
}
