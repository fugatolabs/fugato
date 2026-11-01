package main

import (
	"embed"
	"fmt"
	"io"
)

//go:embed assets/fugato.schema.json assets/native.yaml assets/compose.yaml assets/mixed.yaml
var referenceAssets embed.FS

func printReference(output io.Writer, command string, args []string) error {
	var asset string
	switch command {
	case "schema":
		if len(args) != 0 {
			return fmt.Errorf("usage: fugato schema")
		}
		asset = "assets/fugato.schema.json"
	case "example":
		if len(args) != 1 {
			return fmt.Errorf("usage: fugato example native|compose|mixed")
		}
		switch args[0] {
		case "native", "compose", "mixed":
			asset = "assets/" + args[0] + ".yaml"
		default:
			return fmt.Errorf("unknown example %q; choose native, compose, or mixed", args[0])
		}
	default:
		return fmt.Errorf("unknown reference command %q", command)
	}
	data, err := referenceAssets.ReadFile(asset)
	if err != nil {
		return err
	}
	_, err = output.Write(data)
	return err
}
