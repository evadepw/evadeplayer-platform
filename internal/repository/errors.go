package repository

import "errors"

var ErrNotFound = errors.New("not found")
var ErrEmailTaken = errors.New("email already taken")
