//go:build ci
// +build ci

package crdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/authzed/spicedb/internal/datastore"
	"github.com/authzed/spicedb/pkg/namespace"
)

const (
	testUserNamespace = "test/user"
)

var testUserNS = namespace.Namespace(testUserNamespace)

// newCRDB creates a new database in crdb, migrates to HEAD, and returns the specific crdb datastore.
func (st sqlTest) newCRDB() (*crdbDatastore, error) {
	// Use crdb defaults
	ds, err := st.New(
		5*time.Second,
		24*time.Hour,
		128)
	return ds.(*crdbDatastore), err
}

func executeWithErrors(errors *[]pgconn.PgError, maxRetries int) executeTxRetryFunc {
	return func(ctx context.Context, conn conn, txOptions pgx.TxOptions, fn transactionFn) (err error) {
		wrappedFn := func(tx pgx.Tx) error {
			if len(*errors) > 0 {
				retErr := (*errors)[0]
				(*errors) = (*errors)[1:]
				return &retErr
			}

			return fn(tx)
		}

		return executeWithResets(ctx, conn, txOptions, wrappedFn, maxRetries)
	}
}

func TestTxReset(t *testing.T) {
	cases := []struct {
		name          string
		maxRetries    int
		errors        []pgconn.PgError
		expectError   bool
		expectedError error
	}{
		{
			name:       "retryable",
			maxRetries: 4,
			errors: []pgconn.PgError{
				{Code: crdbRetryErrCode},
				{Code: crdbRetryErrCode},
				{Code: crdbRetryErrCode},
			},
			expectError:   false,
			expectedError: nil,
		},
		{
			name:       "resettable",
			maxRetries: 4,
			errors: []pgconn.PgError{
				{Code: crdbAmbiguousErrorCode},
				{Code: crdbAmbiguousErrorCode},
				{Code: crdbAmbiguousErrorCode},
			},
			expectError:   false,
			expectedError: nil,
		},
		{
			name:       "mixed",
			maxRetries: 50,
			errors: []pgconn.PgError{
				{Code: crdbRetryErrCode},
				{Code: crdbAmbiguousErrorCode},
				{Code: crdbRetryErrCode},
			},
			expectError:   false,
			expectedError: nil,
		},
		{
			name:          "noErrors",
			maxRetries:    50,
			errors:        []pgconn.PgError{},
			expectError:   false,
			expectedError: nil,
		},
		{
			name:       "nonRecoverable",
			maxRetries: 1,
			errors: []pgconn.PgError{
				{Code: crdbRetryErrCode},
				{Code: crdbAmbiguousErrorCode},
			},
			expectError:   true,
			expectedError: errors.New(errReachedMaxRetry),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tester := newTester(crdbContainer, "root:fake", 26257)
			ds, err := tester.newCRDB()
			require.NoError(err)
			ds.execute = executeWithErrors(&tt.errors, tt.maxRetries)

			ctx := context.Background()
			ok, err := ds.IsReady(ctx)
			require.NoError(err)
			require.True(ok)

			// WriteNamespace utilizes execute so we'll use it
			revision, err := ds.WriteNamespace(ctx, testUserNS)
			if tt.expectedError != nil {
				require.Error(err)
				require.Equal(datastore.NoRevision, revision)
			} else {
				require.NoError(err)
				require.True(revision.GreaterThan(decimal.Zero))
			}

			tester.cleanup()
			ds.Close()
		})
	}
}
