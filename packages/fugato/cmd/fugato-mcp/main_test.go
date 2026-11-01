package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

type checkService struct{ err error }

func (s checkService) List(context.Context) ([]control.Runtime, error) {
	return nil, s.err
}

func TestCheckConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "connected"},
		{name: "unauthorized", err: errors.New("daemon API: unauthorized")},
		{name: "unreachable", err: errors.New("connect: connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var status bytes.Buffer
			err := checkConnection(context.Background(), checkService{err: tc.err}, &status)
			if tc.err == nil {
				if err != nil || !strings.Contains(status.String(), "connection OK") {
					t.Fatalf("check result = %v, status = %q", err, status.String())
				}
			} else if !errors.Is(err, tc.err) || status.Len() != 0 {
				t.Fatalf("check result = %v, status = %q", err, status.String())
			}
		})
	}
}
