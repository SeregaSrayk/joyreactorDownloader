package graphql

import "errors"

var (
	ErrTagNotFound  = errors.New("graphql: tag not found")
	ErrUserNotFound = errors.New("graphql: user not found")
)
