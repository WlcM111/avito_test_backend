package pgxx

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL SQLSTATE codes used to classify storage failures.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateCheckViolation      = "23514"
	sqlStateForeignKeyViolation = "23503"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
	sqlStateQueryCanceled       = "57014"
)

func code(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// ConstraintName returns the violated constraint name, or "".
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// IsUniqueViolation reports a duplicate key error.
func IsUniqueViolation(err error) bool { return code(err) == sqlStateUniqueViolation }

// IsCheckViolation reports a violated CHECK constraint.
func IsCheckViolation(err error) bool { return code(err) == sqlStateCheckViolation }

// IsForeignKeyViolation reports a violated foreign key.
func IsForeignKeyViolation(err error) bool { return code(err) == sqlStateForeignKeyViolation }

// IsRetryable reports failures that are worth retrying: serialization
// conflicts and deadlocks.
func IsRetryable(err error) bool {
	c := code(err)
	return c == sqlStateSerializationFail || c == sqlStateDeadlockDetected
}

// IsStatementTimeout reports a statement cancelled by statement_timeout.
func IsStatementTimeout(err error) bool { return code(err) == sqlStateQueryCanceled }
