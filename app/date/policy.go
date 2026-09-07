package date

import (
	"fmt"
	"time"

	"gocordis-csv-collector/app/errs"
	"gocordis-csv-collector/app/model"
)

// Policy values supported by v0.1.
const (
	PolicyYesterday = "yesterday"
	PolicySpecific  = "specific"
)

// Policy resolves which business date a normal run should collect.
type Policy struct {
	Type     string
	Specific model.CollectionDate
	Now      func() time.Time
}

func (p Policy) Resolve() (model.CollectionDate, error) {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	switch p.Type {
	case "", PolicyYesterday:
		return model.NewCollectionDate(now).AddDate(0, 0, -1), nil
	case PolicySpecific:
		if p.Specific.IsZero() {
			return model.CollectionDate{}, fmt.Errorf("%w: specific date policy requires a date", errs.ErrInvalidConfig)
		}
		return p.Specific, nil
	default:
		return model.CollectionDate{}, fmt.Errorf("%w: unknown date_policy %q", errs.ErrInvalidConfig, p.Type)
	}
}
