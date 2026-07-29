package sample

import (
	"fmt"
	"os"
)

// User is a domain type.
type User struct {
	Name string
}

const MaxRetries = 3

var defaultTimeout = 30

// NewUser creates a user.
func NewUser(name string) *User {
	return &User{Name: name}
}

// String renders the user.
func (u *User) String() string {
	return fmt.Sprintf("User(%s)", u.Name)
}
