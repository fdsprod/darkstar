// Package configurationstore defines the persistence boundary for editable configuration.
package configurationstore

import (
	"context"
	"errors"
)

var (
	ErrRevisionConflict = errors.New("configuration revision conflict")
	ErrPathBoundary     = errors.New("configuration path boundary violation")
	ErrNoPrevious       = errors.New("no previous configuration revision")
)

type Target uint8

const (
	TargetUser Target = iota + 1
	TargetProject
)

type Operation uint8

const (
	OperationSet Operation = iota + 1
	OperationUnset
)

type Mutation struct {
	Operation Operation
	Path      []string
	Value     any
}

type Snapshot struct {
	Revision  string
	Present   bool
	Reference string
	Values    map[string]any
}

type SecretReceipt struct {
	Revision string
	Name     string
}

type Store interface {
	Snapshot(context.Context, Target) (Snapshot, error)
	SecretRevision(context.Context) (string, error)
	Preview(context.Context, Target, Mutation) (Snapshot, error)
	Apply(context.Context, Target, Mutation, string) (Snapshot, error)
	Restore(context.Context, Target, string) (Snapshot, error)
	PutSecret(context.Context, string, string, string) (SecretReceipt, error)
}
