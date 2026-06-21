//go:build !system_ladybug

package main

import "fmt"

func openLadybugGraph(string, bool) (graphHandle, error) {
	return nil, fmt.Errorf("Ladybug support requires building with -tags system_ladybug")
}
