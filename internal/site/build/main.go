// Command build writes the documentation site's sources.
//
//	go run ./internal/site/build <output-dir>
//
// The site is generated rather than served from docs/ directly; internal/site
// says why.
package main

import (
	"fmt"
	"os"

	"github.com/suruseas/opossum/internal/site"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/site/build <output-dir>")
		os.Exit(2)
	}
	if err := site.Build(".", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "building the site:", err)
		os.Exit(1)
	}
	fmt.Println("built", len(site.Pages)+1, "pages into", os.Args[1])
}
