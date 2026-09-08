package app

import "errors"

// Errors raised by the use cases around the merchant directory. They are kept
// in the application layer because they describe an integration outcome, not a
// rule of the catalogue domain.
var (
	// ErrMerchantUnauthenticated means the presented access key is unknown,
	// revoked or belongs to a merchant disconnected by the operator.
	ErrMerchantUnauthenticated = errors.New("merchant is not authenticated")
	// ErrMerchantDirectoryUnavailable means merchant-service could not be
	// reached or did not answer in time. It is deliberately distinct from
	// ErrMerchantUnauthenticated: answering 401 on an outage would tell the
	// merchant its key is invalid, which is false.
	ErrMerchantDirectoryUnavailable = errors.New("merchant directory is unavailable")
)
