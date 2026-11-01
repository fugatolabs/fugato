package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBundledSchemaMatchesReference(t *testing.T) {
	want, err := os.ReadFile("../../../../docs/reference/fugato.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runTo([]string{"schema"}, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatal("bundled schema drifted from docs/reference/fugato.schema.json")
	}
}

func TestExampleCommandsNeedNoProjectOrDaemon(t *testing.T) {
	for _, name := range []string{"native", "compose", "mixed"} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := runTo([]string{"example", name}, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "version: 1\n") || !strings.Contains(output.String(), "start:\n") {
				t.Fatalf("incomplete %s example: %s", name, output.String())
			}
		})
	}
}
